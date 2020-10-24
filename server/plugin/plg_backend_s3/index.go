package plg_backend_s3

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"
	"github.com/mickael-kerjean/filestash/server/common"
)

var s3Cache common.AppCache

type s3Backend struct {
	client *s3.S3
	config *aws.Config
	params map[string]string
}

func init() {
	common.Backend.Register("s3", s3Backend{})
	s3Cache = common.NewAppCache(2, 1)
}

func (s s3Backend) Init(params map[string]string, app *common.App) (common.IBackend, error) {
	if params["encryption_key"] != "" && len(params["encryption_key"]) != 32 {
		return nil, common.NewError(fmt.Sprintf("Encryption key needs to be 32 characters (current: %d)", len(params["encryption_key"])), 400)
	}

	if params["region"] == "" {
		params["region"] = "us-east-2"
	}
	config := &aws.Config{
		Credentials:      credentials.NewStaticCredentials(params["access_key_id"], params["secret_access_key"], params["session_token"]),
		S3ForcePathStyle: aws.Bool(true),
		Region:           aws.String(params["region"]),
	}
	if params["endpoint"] != "" {
		config.Endpoint = aws.String(params["endpoint"])
	}
	backend := &s3Backend{
		config: config,
		params: params,
		client: s3.New(session.New(config)),
	}
	return backend, nil
}

func (s s3Backend) LoginForm() common.Form {
	return common.Form{
		Elmnts: []common.FormElement{
			{
				Name:  "type",
				Type:  "hidden",
				Value: "s3",
			},
			{
				Name:        "access_key_id",
				Type:        "text",
				Placeholder: "Access Key ID*",
			},
			{
				Name:        "secret_access_key",
				Type:        "text",
				Placeholder: "Secret Access Key*",
			},
			{
				Name:        "advanced",
				Type:        "enable",
				Placeholder: "Advanced",
				Target:      []string{"s3_path", "s3_session_token", "s3_encryption_key", "s3_region", "s3_endpoint"},
			},
			{
				Id:          "s3_session_token",
				Name:        "session_token",
				Type:        "text",
				Placeholder: "Session Token",
			},
			{
				Id:          "s3_path",
				Name:        "path",
				Type:        "text",
				Placeholder: "Path",
			},
			{
				Id:          "s3_encryption_key",
				Name:        "encryption_key",
				Type:        "text",
				Placeholder: "Encryption Key",
			},
			{
				Id:          "s3_region",
				Name:        "region",
				Type:        "text",
				Placeholder: "Region",
			},
			{
				Id:          "s3_endpoint",
				Name:        "endpoint",
				Type:        "text",
				Placeholder: "Endpoint",
			},
		},
	}
}

func (s s3Backend) Meta(path string) common.Metadata {
	if path == "/" {
		return common.Metadata{
			CanCreateFile: common.NewBool(false),
			CanRename:     common.NewBool(false),
			CanMove:       common.NewBool(false),
			CanUpload:     common.NewBool(false),
		}
	}
	return common.Metadata{}
}

func (s s3Backend) Ls(path string) (files []os.FileInfo, err error) {
	files = make([]os.FileInfo, 0)
	p := s.path(path)

	if p.bucket == "" {
		b, err := s.client.ListBuckets(&s3.ListBucketsInput{})
		if err != nil {
			return nil, err
		}
		for _, bucket := range b.Buckets {
			files = append(files, &common.File{
				FName:   *bucket.Name,
				FType:   "directory",
				FTime:   bucket.CreationDate.Unix(),
				CanMove: common.NewBool(false),
			})
		}
		return files, nil
	}
	client := s3.New(s.createSession(p.bucket))

	objs, errTmp := client.ListObjectsV2(&s3.ListObjectsV2Input{
		Bucket:    aws.String(p.bucket),
		Prefix:    aws.String(p.path),
		Delimiter: aws.String("/"),
	})
	if errTmp != nil {
		err = errTmp
		return
	}
	for i, object := range objs.Contents {
		if i == 0 && *object.Key == p.path {
			continue
		}
		files = append(files, &common.File{
			FName: filepath.Base(*object.Key),
			FType: "file",
			FTime: object.LastModified.Unix(),
			FSize: *object.Size,
		})
	}
	for _, object := range objs.CommonPrefixes {
		files = append(files, &common.File{
			FName: filepath.Base(*object.Prefix),
			FType: "directory",
		})
	}

	return files, err
}

func (s s3Backend) Cat(path string) (io.ReadCloser, error) {
	p := s.path(path)
	client := s3.New(s.createSession(p.bucket))

	input := &s3.GetObjectInput{
		Bucket: aws.String(p.bucket),
		Key:    aws.String(p.path),
	}
	if s.params["encryption_key"] != "" {
		input.SSECustomerAlgorithm = aws.String("AES256")
		input.SSECustomerKey = aws.String(s.params["encryption_key"])
	}
	obj, err := client.GetObject(input)
	if err != nil {
		awsErr, ok := err.(awserr.Error)
		if ok == false {
			return nil, err
		}
		if awsErr.Code() == "InvalidRequest" && strings.Contains(awsErr.Message(), "encryption") {
			input.SSECustomerAlgorithm = nil
			input.SSECustomerKey = nil
			obj, err = client.GetObject(input)
			return obj.Body, err
		} else if awsErr.Code() == "InvalidArgument" && strings.Contains(awsErr.Message(), "secret key was invalid") {
			return nil, common.NewError("This file is encrypted file, you need the correct key!", 400)
		} else if awsErr.Code() == "AccessDenied" {
			return nil, common.ErrNotAllowed
		}
		return nil, err
	}

	return obj.Body, nil
}

func (s s3Backend) Mkdir(path string) error {
	p := s.path(path)
	client := s3.New(s.createSession(p.bucket))

	if p.path == "" {
		_, err := client.CreateBucket(&s3.CreateBucketInput{
			Bucket: aws.String(path),
		})
		return err
	}
	_, err := client.PutObject(&s3.PutObjectInput{
		Bucket: aws.String(p.bucket),
		Key:    aws.String(p.path),
	})
	return err
}

func (s s3Backend) Rm(path string) error {
	p := s.path(path)
	client := s3.New(s.createSession(p.bucket))
	if p.bucket == "" {
		return common.ErrNotFound
	} else if strings.HasSuffix(path, "/") == false {
		// If this is an explicit object path instead of a folder
		_, err := client.DeleteObject(&s3.DeleteObjectInput{
			Bucket: aws.String(p.bucket),
			Key:    aws.String(p.path),
		})
		return err
	}

	objs, err := client.ListObjectsV2(&s3.ListObjectsV2Input{
		Bucket:    aws.String(p.bucket),
		Prefix:    aws.String(p.path),
		Delimiter: aws.String("/"),
	})
	if err != nil {
		return err
	}

	// Delete the object children of path/
	for _, obj := range objs.Contents {
		_, err := client.DeleteObject(&s3.DeleteObjectInput{
			Bucket: aws.String(p.bucket),
			Key:    obj.Key,
		})
		if err != nil {
			return err
		}
	}

	// Recursively delete sub-folders
	for _, pref := range objs.CommonPrefixes {
		s.Rm("/" + p.bucket + "/" + *pref.Prefix)
		_, err := client.DeleteObject(&s3.DeleteObjectInput{
			Bucket: aws.String(p.bucket),
			Key:    pref.Prefix,
		})
		if err != nil {
			return err
		}
	}

	if p.path == "" {
		_, err := client.DeleteBucket(&s3.DeleteBucketInput{
			Bucket: aws.String(p.bucket),
		})
		return err
	}

	/*
		Finally, delete the null "folder" object.
		
		Note that in proper S3 implementations, null "folders" are automatically
		deleted when their last real child object is removed, so this should be
		unnecessary.  This operation will usually be a noop, but was included to
		maintain compatibility with incorrect or sloppy third-party S3 implementations.
	*/ 
	_, err = client.DeleteObject(&s3.DeleteObjectInput{
		Bucket: aws.String(p.bucket),
		Key:    aws.String(p.path),
	})
	return err
}

func (s s3Backend) Mv(from string, to string) error {
	f := s.path(from)
	t := s.path(to)
	client := s3.New(s.createSession(f.bucket))

	if f.path == "" || strings.HasSuffix(from, "/") {
		return common.ErrNotImplemented
	}

	input := &s3.CopyObjectInput{
		Bucket:     aws.String(t.bucket),
		CopySource: aws.String(f.bucket + "/" + f.path),
		Key:        aws.String(t.path),
	}
	if s.params["encryption_key"] != "" {
		input.CopySourceSSECustomerAlgorithm = aws.String("AES256")
		input.CopySourceSSECustomerKey = aws.String(s.params["encryption_key"])
		input.SSECustomerAlgorithm = aws.String("AES256")
		input.SSECustomerKey = aws.String(s.params["encryption_key"])
	}

	_, err := client.CopyObject(input)
	if err != nil {
		return err
	}
	return s.Rm(from)
}

func (s s3Backend) Touch(path string) error {
	p := s.path(path)
	client := s3.New(s.createSession(p.bucket))

	if p.bucket == "" {
		return common.ErrNotValid
	}

	input := &s3.PutObjectInput{
		Body:          strings.NewReader(""),
		ContentLength: aws.Int64(0),
		Bucket:        aws.String(p.bucket),
		Key:           aws.String(p.path),
	}
	if s.params["encryption_key"] != "" {
		input.SSECustomerAlgorithm = aws.String("AES256")
		input.SSECustomerKey = aws.String(s.params["encryption_key"])
	}
	_, err := client.PutObject(input)
	return err
}

func (s s3Backend) Save(path string, file io.Reader) error {
	p := s.path(path)

	if p.bucket == "" {
		return common.ErrNotValid
	}
	uploader := s3manager.NewUploader(s.createSession(path))
	input := s3manager.UploadInput{
		Body:   file,
		Bucket: aws.String(p.bucket),
		Key:    aws.String(p.path),
	}
	if s.params["encryption_key"] != "" {
		input.SSECustomerAlgorithm = aws.String("AES256")
		input.SSECustomerKey = aws.String(s.params["encryption_key"])
	}
	_, err := uploader.Upload(&input)
	return err
}

func (s s3Backend) createSession(bucket string) *session.Session {
	params := s.params
	params["bucket"] = bucket
	c := s3Cache.Get(params)
	if c == nil {
		res, err := s.client.GetBucketLocation(&s3.GetBucketLocationInput{
			Bucket: aws.String(bucket),
		})
		if err != nil {
			s.config.Region = aws.String("us-east-1")
		} else {
			if res.LocationConstraint == nil {
				s.config.Region = aws.String("us-east-1")
			} else {
				s.config.Region = res.LocationConstraint
			}
		}
		s3Cache.Set(params, s.config.Region)
	} else {
		s.config.Region = c.(*string)
	}

	sess := session.New(s.config)
	return sess
}

type s3Path struct {
	bucket string
	path   string
}

func (s s3Backend) path(p string) s3Path {
	sp := strings.Split(p, "/")
	bucket := ""
	if len(sp) > 1 {
		bucket = sp[1]
	}
	path := ""
	if len(sp) > 2 {
		path = strings.Join(sp[2:], "/")
	}

	return s3Path{
		bucket,
		path,
	}
}
