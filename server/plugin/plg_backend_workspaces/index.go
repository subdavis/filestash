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

	"github.com/mickael-kerjean/filestash/server/common"
	s3 "github.com/mickael-kerjean/filestash/server/plugin/plg_backend_s3"
)

var wioCache common.AppCache

// UserResponse user response
type UserResponse struct {
	Username string `json:"username"`
}

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
	Owner    UserResponse `json:"owner"`
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

// TokenSearchResponseWorkspacePart part of TokenSearchResponse
type TokenSearchResponseWorkspacePart struct {
	Path      string            `json:"path"`
	Workspace WorkspaceResponse `json:"workspace"`
}

// TokenSearchResponse token search response
type TokenSearchResponse struct {
	Tokens     []TokenNodeWrapper                          `json:"tokens"`
	Workspaces map[string]TokenSearchResponseWorkspacePart `json:"workspaces"`
}

// WorkspacesBackend main struct
type WorkspacesBackend struct {
	app    *common.App
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
	// u, _ := url.ParseRequestURI(params["endpoint"])
	// u.Path = "/api/auth/jwt/login"
	// resp, err := httpClient.PostForm(fmt.Sprintf("%v", u),
	// 	url.Values{"username": {params["username"]}, "password": {params["password"]}})
	// if err != nil {
	// 	return nil, err
	// }
	// defer resp.Body.Close()
	// data := AuthResponse{}
	// err = json.NewDecoder(resp.Body).Decode(&data)
	// if err != nil {
	// 	return nil, err
	// }

	common.Log.Debug("New Workspaces Client created")

	// Persist new configuration
	w.app = app
	// w.token = data.AccessToken
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

// Meta describes capabilities for a path
func (w WorkspacesBackend) Meta(path string) common.Metadata {
	common.Log.Info("PATH META %s", path)
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

// Cat get bytes
func (w WorkspacesBackend) Cat(path string) (io.ReadCloser, error) {
	p, s3backend, initErr := w.s3Init([]string{path})
	if initErr != nil {
		return nil, initErr
	}
	return s3backend.Cat(p)
}

// Mkdir make directory
func (w WorkspacesBackend) Mkdir(path string) error {
	p, s3backend, initErr := w.s3Init([]string{path})
	if initErr != nil {
		return initErr
	}
	return s3backend.Mkdir(p)
}

// Rm Remove
func (w WorkspacesBackend) Rm(path string) error {
	p, s3backend, initErr := w.s3Init([]string{path})
	if initErr != nil {
		return initErr
	}
	return s3backend.Rm(p)
}

// Mv move
func (w WorkspacesBackend) Mv(from string, to string) error {
	return nil
}

// Touch make empty object
func (w WorkspacesBackend) Touch(path string) error {
	p, s3backend, initErr := w.s3Init([]string{path})
	if initErr != nil {
		return initErr
	}
	return s3backend.Touch(p)
}

// Save bytes
func (w WorkspacesBackend) Save(path string, file io.Reader) error {
	p, s3backend, initErr := w.s3Init([]string{path})
	if initErr != nil {
		return initErr
	}
	return s3backend.Save(p, file)
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
		wsPrefix := ""
		if ws.Workspace.Root.RootType != "unmanaged" {
			wsPrefix = fmt.Sprintf("%s/%s", ws.Workspace.Owner.Username, ws.Workspace.Name)
		} else {
			wsPrefix = ws.Workspace.BasePath
		}
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
		p := path.Join(ws.Workspace.Root.Bucket, ws.Workspace.Root.BasePath, wsPrefix)
		p = fmt.Sprintf("/%s", path.Join(p, ws.Path)) // Always include root (prefix) slash
		common.Log.Info("PATH %s", p)
		return p, backend.(*s3.S3Backend), nil
	}
	return "", nil, fmt.Errorf("Something bad")
}

func (w WorkspacesBackend) request(method string, path string, body []byte, data interface{}) error {
	u, _ := url.ParseRequestURI(w.params["endpoint"])
	u.Path = path
	req, _ := http.NewRequest(method, fmt.Sprintf("%v", u), nil)
	common.Log.Info(fmt.Sprintf("%v", w.params))
	req.SetBasicAuth(w.params["username"], w.params["password"])
	// req.Header.Add("Authorization", fmt.Sprintf("Bearer %v", w.token))
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
