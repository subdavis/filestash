package plg_backend_workspaces

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"

	"github.com/mickael-kerjean/filestash/server/common"
	s3 "github.com/mickael-kerjean/filestash/server/plugin/plg_backend_s3"
)

var wioCache common.AppCache

// RootResponse root response
type RootResponse struct {
	RootType string `json:"root_type"`
	Bucket   string `json:"bucket"`
	BasePath string `json:"base_path"`
	ID       string `json:"id"`
}

// WorkspaceResponse API Response
type WorkspaceResponse struct {
	Name     string       `json:"name"`
	BasePath string       `json:"base_path"`
	ID       string       `json:"id"`
	Created  string       `json:"created"`
	OwnerID  string       `json:"owner_id"`
	RootID   string       `json:"root_id"`
	Root     RootResponse `json:"root"`
}

// TokenResponse token response
type TokenResponse struct {
	Expiration   string `json:"expiration"`
	ID           string `json:"id"`
	Created      string `json:"created"`
	AccessKey    string `json:"access_key_id"`
	SecretKey    string `json:"secret_access_key"`
	SessionToken string `json:"session_token"`
}

// NodeResponse node response
type NodeResponse struct {
	Name   string `json:"name"`
	APIURL string `json:"api_url"`
	Region string `json:"region_name"`
}

// TokenNodeWrapper token node wrapper
type TokenNodeWrapper struct {
	Token TokenResponse `json:"token"`
	Node  NodeResponse  `json:"node"`
}

type TokenSearchResponseWorkspacePart struct {
	Path      string            `json:"path"`
	Workspace WorkspaceResponse `json:"workspace"`
}

// TokenSearchResponse token search response
type TokenSearchResponse struct {
	Tokens     []TokenNodeWrapper                          `json:"tokens"`
	Workspaces map[string]TokenSearchResponseWorkspacePart `json:"workspaces"`
}

// AuthResponse login response
type AuthResponse struct {
	AccessToken string `json:"access_token"`
}

// WorkspacesBackend main struct
type WorkspacesBackend struct {
	app    *common.App
	token  string
	client *http.Client
	params map[string]string
}

func init() {
	common.Backend.Register("workspaces", WorkspacesBackend{})
	wioCache = common.NewAppCache(2, 1)
}

// Init initializes an instance of the Worksapce backend
func (w WorkspacesBackend) Init(params map[string]string, app *common.App) (common.IBackend, error) {
	w.params = params

	// Try to load from cache
	if obj := wioCache.Get(params); obj != nil {
		return obj.(*WorkspacesBackend), nil
	}

	// Construct a new Wio Client
	httpClient := &http.Client{}

	// Fetch Credentials
	u, _ := url.ParseRequestURI(params["endpoint"])
	u.Path = "/api/auth/jwt/login"
	resp, err := httpClient.PostForm(fmt.Sprintf("%v", u),
		url.Values{"username": {params["username"]}, "password": {params["password"]}})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data := AuthResponse{}
	err = json.NewDecoder(resp.Body).Decode(&data)
	if err != nil {
		return nil, err
	}

	common.Log.Debug("New Workspaces Client created")

	// Persist new configuration
	w.app = app
	w.token = data.AccessToken
	w.client = httpClient
	w.params = params

	// Set cache
	wioCache.Set(params, &w)
	return w, nil
}

// LoginForm returns form elements for UI
func (w WorkspacesBackend) LoginForm() common.Form {
	return common.Form{
		Elmnts: []common.FormElement{
			{
				Name:  "type",
				Type:  "hidden",
				Value: "workspaces",
			},
			{
				Name:        "username",
				Type:        "text",
				Placeholder: "Username",
				Required:    true,
			},
			{
				Name:        "password",
				Type:        "text",
				Placeholder: "Password",
				Required:    true,
			},
			{
				Name:        "advanced",
				Type:        "enable",
				Placeholder: "Advanced",
				Target:      []string{"wio_endpoint"},
			},
			{
				Id:          "wio_endpoint",
				Name:        "endpoint",
				Type:        "text",
				Placeholder: "Endpoint",
			},
		},
	}
}

// Meta what does this do
func (w WorkspacesBackend) Meta(path string) common.Metadata {
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

func (w WorkspacesBackend) request(method string, path string, body []byte, data interface{}) error {
	u, _ := url.ParseRequestURI(w.params["endpoint"])
	u.Path = path
	req, _ := http.NewRequest(method, fmt.Sprintf("%v", u), nil)
	req.Header.Add("Authorization", fmt.Sprintf("Bearer %v", w.token))
	if body != nil {
		bodyReader := bytes.NewBuffer(body)
		bodyCloser := ioutil.NopCloser(bodyReader)
		req.Body = bodyCloser
	}
	resp, err := w.client.Do(req)
	if err != nil {
		return err
	}
	err = json.NewDecoder(resp.Body).Decode(data)
	return err
}

func (w WorkspacesBackend) s3Init(terms []string) (string, *s3.S3Backend, error) {
	data := TokenSearchResponse{}
	jsonValue, _ := json.Marshal(map[string][]string{"search_terms": terms})
	reqErr := w.request("POST", "/api/token/search", jsonValue, &data)
	if reqErr != nil {
		return "", nil, reqErr
	}
	for key := range data.Workspaces {
		ws := data.Workspaces[key]
		s3config := s3.S3Backend{}
		s3params := map[string]string{
			"access_key_id":     data.Tokens[0].Token.AccessKey,
			"secret_access_key": data.Tokens[0].Token.SecretKey,
			"session_token":     data.Tokens[0].Token.SessionToken,
			"path":              "",
			"region":            data.Tokens[0].Node.Region,
			"endpoint":          data.Tokens[0].Node.APIURL,
			"encryption_key":    "",
		}
		backend, err := s3config.Init(s3params, w.app)
		if err != nil {
			return "", nil, err
		}
		p := path.Join(ws.Workspace.Root.Bucket, ws.Workspace.Root.BasePath, ws.Workspace.BasePath)
		p = fmt.Sprintf("/%s", path.Join(p, ws.Path)) // Always include root slash
		return p, backend.(*s3.S3Backend), nil
	}
	return "", nil, fmt.Errorf("Something bad")
}

// Ls gets workspace dir contents
func (w WorkspacesBackend) Ls(path string) (files []os.FileInfo, err error) {
	files = make([]os.FileInfo, 0)

	// Fetch workspace list if no path
	if path == "/" {
		data := []WorkspaceResponse{}
		reqErr := w.request("GET", "/api/workspace", nil, &data)
		if reqErr != nil {
			return nil, reqErr
		}

		for _, ws := range data {
			// created, err := time.Parse("2020-09-09T20:43:54.368144", ws.created)
			files = append(files, &common.File{
				FName:   ws.Name,
				FType:   "directory",
				FTime:   0,
				CanMove: common.NewBool(false),
			})
		}
		return files, nil
	}
	p, s3backend, initErr := w.s3Init([]string{path})
	if initErr != nil {
		return nil, initErr
	}
	p = fmt.Sprintf("%s/", p) // always include trailing slash
	return s3backend.Ls(p)
}

func (w WorkspacesBackend) Cat(path string) (io.ReadCloser, error) {
	// p := s.path(path)
	// client := s3.New(s.createSession(p.bucket))

	// input := &s3.GetObjectInput{
	// 	Bucket: aws.String(p.bucket),
	// 	Key:    aws.String(p.path),
	// }
	// if s.params["encryption_key"] != "" {
	// 	input.SSECustomerAlgorithm = aws.String("AES256")
	// 	input.SSECustomerKey = aws.String(s.params["encryption_key"])
	// }
	// obj, err := client.GetObject(input)
	// if err != nil {
	// 	awsErr, ok := err.(awserr.Error)
	// 	if ok == false {
	// 		return nil, err
	// 	}
	// 	if awsErr.Code() == "InvalidRequest" && strings.Contains(awsErr.Message(), "encryption") {
	// 		input.SSECustomerAlgorithm = nil
	// 		input.SSECustomerKey = nil
	// 		obj, err = client.GetObject(input)
	// 		return obj.Body, err
	// 	} else if awsErr.Code() == "InvalidArgument" && strings.Contains(awsErr.Message(), "secret key was invalid") {
	// 		return nil, NewError("This file is encrypted file, you need the correct key!", 400)
	// 	} else if awsErr.Code() == "AccessDenied" {
	// 		return nil, ErrNotAllowed
	// 	}
	// 	return nil, err
	// }
	stringReader := strings.NewReader("shiny!")
	stringReadCloser := ioutil.NopCloser(stringReader)
	return stringReadCloser, nil
}

func (w WorkspacesBackend) Mkdir(path string) error {
	// p := s.path(path)
	// client := s3.New(s.createSession(p.bucket))

	// if p.path == "" {
	// 	_, err := client.CreateBucket(&s3.CreateBucketInput{
	// 		Bucket: aws.String(path),
	// 	})
	// 	return err
	// }
	// _, err := client.PutObject(&s3.PutObjectInput{
	// 	Bucket: aws.String(p.bucket),
	// 	Key:    aws.String(p.path),
	// })
	// return err
	return nil
}

func (w WorkspacesBackend) Rm(path string) error {
	// p := s.path(path)
	// client := s3.New(s.createSession(p.bucket))
	// if p.bucket == "" {
	// 	return ErrNotFound
	// } else if strings.HasSuffix(path, "/") == false {
	// 	_, err := client.DeleteObject(&s3.DeleteObjectInput{
	// 		Bucket: aws.String(p.bucket),
	// 		Key:    aws.String(p.path),
	// 	})
	// 	return err
	// }

	// objs, err := client.ListObjects(&s3.ListObjectsInput{
	// 	Bucket:    aws.String(p.bucket),
	// 	Prefix:    aws.String(p.path),
	// 	Delimiter: aws.String("/"),
	// })
	// if err != nil {
	// 	return err
	// }
	// for _, obj := range objs.Contents {
	// 	_, err := client.DeleteObject(&s3.DeleteObjectInput{
	// 		Bucket: aws.String(p.bucket),
	// 		Key:    obj.Key,
	// 	})
	// 	if err != nil {
	// 		return err
	// 	}
	// }
	// for _, pref := range objs.CommonPrefixes {
	// 	s.Rm("/" + p.bucket + "/" + *pref.Prefix)
	// 	_, err := client.DeleteObject(&s3.DeleteObjectInput{
	// 		Bucket: aws.String(p.bucket),
	// 		Key:    pref.Prefix,
	// 	})
	// 	if err != nil {
	// 		return err
	// 	}
	// }

	// if p.path == "" {
	// 	_, err := client.DeleteBucket(&s3.DeleteBucketInput{
	// 		Bucket: aws.String(p.bucket),
	// 	})
	// 	return err
	// }
	// _, err = client.DeleteObject(&s3.DeleteObjectInput{
	// 	Bucket: aws.String(p.bucket),
	// 	Key:    aws.String(p.path),
	// })
	// return err
	return nil
}

func (w WorkspacesBackend) Mv(from string, to string) error {
	// f := s.path(from)
	// t := s.path(to)
	// client := s3.New(s.createSession(f.bucket))

	// if f.path == "" || strings.HasSuffix(from, "/") {
	// 	return ErrNotImplemented
	// }

	// input := &s3.CopyObjectInput{
	// 	Bucket:     aws.String(t.bucket),
	// 	CopySource: aws.String(f.bucket + "/" + f.path),
	// 	Key:        aws.String(t.path),
	// }
	// if s.params["encryption_key"] != "" {
	// 	input.CopySourceSSECustomerAlgorithm = aws.String("AES256")
	// 	input.CopySourceSSECustomerKey = aws.String(s.params["encryption_key"])
	// 	input.SSECustomerAlgorithm = aws.String("AES256")
	// 	input.SSECustomerKey = aws.String(s.params["encryption_key"])
	// }

	// _, err := client.CopyObject(input)
	// if err != nil {
	// 	return err
	// }
	// return s.Rm(from)
	return nil
}

func (w WorkspacesBackend) Touch(path string) error {
	// p := s.path(path)
	// client := s3.New(s.createSession(p.bucket))

	// if p.bucket == "" {
	// 	return ErrNotValid
	// }

	// input := &s3.PutObjectInput{
	// 	Body:          strings.NewReader(""),
	// 	ContentLength: aws.Int64(0),
	// 	Bucket:        aws.String(p.bucket),
	// 	Key:           aws.String(p.path),
	// }
	// if s.params["encryption_key"] != "" {
	// 	input.SSECustomerAlgorithm = aws.String("AES256")
	// 	input.SSECustomerKey = aws.String(s.params["encryption_key"])
	// }
	// _, err := client.PutObject(input)
	// return err
	return nil
}

func (w WorkspacesBackend) Save(path string, file io.Reader) error {
	// p := s.path(path)

	// if p.bucket == "" {
	// 	return ErrNotValid
	// }
	// uploader := s3manager.NewUploader(s.createSession(path))
	// input := s3manager.UploadInput{
	// 	Body:   file,
	// 	Bucket: aws.String(p.bucket),
	// 	Key:    aws.String(p.path),
	// }
	// if s.params["encryption_key"] != "" {
	// 	input.SSECustomerAlgorithm = aws.String("AES256")
	// 	input.SSECustomerKey = aws.String(s.params["encryption_key"])
	// }
	// _, err := uploader.Upload(&input)
	// return err
	return nil
}

// type WioPath struct {
// 	workspace string
// 	path   string
// }

// func (w WorkspacesBackend) path(p string) WioPath {
// 	sp := strings.Split(p, "/")
// 	bucket := ""
// 	if len(sp) > 1 {
// 		bucket = sp[1]
// 	}
// 	path := ""
// 	if len(sp) > 2 {
// 		path = strings.Join(sp[2:], "/")
// 	}

// 	return S3Path{
// 		bucket,
// 		path,
// 	}
// }
