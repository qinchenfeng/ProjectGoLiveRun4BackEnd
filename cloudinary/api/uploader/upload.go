// Package uploader is used for accessing Cloudinary Upload API functionality.
//
//https://cloudinary.com/documentation/image_upload_api_reference
package uploader

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"time"

	"github.com/google/uuid"
	"paotui.sg/cloudinary/api"
	"paotui.sg/cloudinary/config"

	"crypto/tls"
	"paotui.sg/cloudinary/logger"
)

// API is the Upload API main struct.
type API struct {
	Config config.Configuration
	Logger *logger.Logger
	client http.Client
}

// New creates a new Admin API instance from the environment variable.
func New() (*API, error) {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	fmt.Println("set insecureskipverify: true")
	c, err := config.New()
	if err != nil {
		return nil, err
	}
	return &API{
		Config: *c,
		client: http.Client{Transport: tr},
		Logger: logger.New(),
	}, nil
}

func (u *API) callUploadAPI(ctx context.Context, path interface{}, requestParams interface{}, result interface{}) error {
	formParams, err := api.StructToParams(requestParams)
	if err != nil {
		return err
	}

	return u.callUploadAPIWithParams(ctx, api.BuildPath(getAssetType(requestParams), path), formParams, result)
}

func (u *API) callUploadAPIWithParams(ctx context.Context, path string, formParams url.Values, result interface{}) error {
	resp, err := u.postAndSignForm(ctx, path, formParams)
	if err != nil {
		return err
	}

	u.Logger.Debug(string(resp))

	err = json.Unmarshal(resp, result)

	return err

}

func (u *API) postAndSignForm(ctx context.Context, urlPath string, formParams url.Values) ([]byte, error) {
	formParams, err := u.signRequest(formParams)
	if err != nil {
		return nil, err
	}

	return u.postForm(ctx, urlPath, formParams)
}

func (u *API) signRequest(requestParams url.Values) (url.Values, error) {
	if u.Config.Cloud.APISecret == "" {
		return nil, errors.New("must provide API Secret")
	}
	// https://cloudinary.com/documentation/upload_images#generating_authentication_signatures
	// All parameters added to the method call should be included except: file, cloud_name, resource_type and your api_key.
	signatureParams := make(url.Values)
	for k, v := range requestParams {
		switch k {
		case "file", "cloud_name", "resource_type", "api_key":
			// omit
		default:
			signatureParams[k] = v
		}
	}

	signature, err := api.SignParameters(signatureParams, u.Config.Cloud.APISecret)
	if err != nil {
		return nil, err
	}
	requestParams.Set("timestamp", signatureParams.Get("timestamp"))
	requestParams.Add("signature", signature)
	requestParams.Add("api_key", u.Config.Cloud.APIKey)

	return requestParams, nil
}

func (u *API) postForm(ctx context.Context, urlPath interface{}, formParams url.Values) ([]byte, error) {
	bodyBuf := new(bytes.Buffer)
	_, err := bodyBuf.Write([]byte(formParams.Encode()))
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(u.Config.API.Timeout)*time.Second)
	defer cancel()

	return u.postBody(ctx, urlPath, bodyBuf, nil)
}

func (u *API) postFile(ctx context.Context, file interface{}, formParams url.Values) ([]byte, error) {
	unsigned, _ := strconv.ParseBool(formParams.Get("unsigned"))

	if !unsigned {
		var err error
		formParams, err = u.signRequest(formParams)
		if err != nil {
			return nil, err
		}
	}

	uploadEndpoint := api.BuildPath(api.Auto, upload)
	switch fileValue := file.(type) {
	case string:
		if !api.IsLocalFilePath(file) {
			// Can be URL, Base64 encoded string, etc.
			formParams.Add("file", fileValue)

			return u.postForm(ctx, uploadEndpoint, formParams)
		}

		return u.postLocalFile(ctx, uploadEndpoint, fileValue, formParams)
	case io.Reader:
		return u.postIOReader(ctx, uploadEndpoint, fileValue, "file", formParams, map[string]string{}, 0)
	default:
		return nil, errors.New("unsupported file type")
	}
}

// postLocalFile creates a new file upload http request with optional extra params.
func (u *API) postLocalFile(ctx context.Context, urlPath string, filePath string, formParams url.Values) ([]byte, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}

	defer api.DeferredClose(file)

	fi, err := file.Stat()
	if err != nil {
		return nil, err
	}

	if fi.Size() > u.Config.API.ChunkSize {
		return u.postLargeFile(ctx, urlPath, file, formParams)
	}

	return u.postIOReader(ctx, urlPath, file, fi.Name(), formParams, map[string]string{}, 0)
}

func (u *API) postLargeFile(ctx context.Context, urlPath string, file *os.File, formParams url.Values) ([]byte, error) {
	fi, err := file.Stat()
	if err != nil {
		return nil, err
	}

	headers := map[string]string{
		"X-Unique-Upload-Id": randomPublicID(),
	}

	var res []byte

	fileSize := fi.Size()
	var currPos int64 = 0
	for currPos < fileSize {
		currChunkSize := min(fileSize-currPos, u.Config.API.ChunkSize)

		headers["Content-Range"] = fmt.Sprintf("bytes %v-%v/%v", currPos, currPos+currChunkSize-1, fileSize)

		res, err = u.postIOReader(ctx, urlPath, file, fi.Name(), formParams, headers, currChunkSize)
		if err != nil {
			return nil, err
		}

		currPos += currChunkSize
	}

	return res, nil
}

func (u *API) postIOReader(ctx context.Context, urlPath string, reader io.Reader, name string, formParams url.Values, headers map[string]string, chunkSize int64) ([]byte, error) {
	bodyBuf := new(bytes.Buffer)
	formWriter := multipart.NewWriter(bodyBuf)

	headers["Content-Type"] = formWriter.FormDataContentType()

	for key, val := range formParams {
		_ = formWriter.WriteField(key, val[0])
	}

	partWriter, err := formWriter.CreateFormFile("file", name)
	if err != nil {
		return nil, err
	}

	if chunkSize != 0 {
		_, err = io.CopyN(partWriter, reader, chunkSize)
	} else {
		_, err = io.Copy(partWriter, reader)
	}
	if err != nil {
		return nil, err
	}

	err = formWriter.Close()
	if err != nil {
		return nil, err
	}

	if u.Config.API.UploadTimeout != 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(u.Config.API.UploadTimeout)*time.Second)
		defer cancel()
	}

	return u.postBody(ctx, urlPath, bodyBuf, headers)
}

func (u *API) postBody(ctx context.Context, urlPath interface{}, bodyBuf *bytes.Buffer, headers map[string]string) ([]byte, error) {

	req, err := http.NewRequest(http.MethodPost,
		u.getUploadURL(urlPath),
		bodyBuf,
	)
	if err != nil {
		return nil, err
	}

	req.Header.Set("User-Agent", api.UserAgent)
	for key, val := range headers {
		req.Header.Add(key, val)
	}

	req = req.WithContext(ctx)
	fmt.Println("->->->u.client.Do(req)")
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	u.client.Transport = tr
	fmt.Println("->->->set insecureskipverify: true")
	resp, err := u.client.Do(req)
	if err != nil {
		return nil, err
	}

	defer api.DeferredClose(resp.Body)

	return ioutil.ReadAll(resp.Body)
}

func (u *API) getUploadURL(urlPath interface{}) string {
	return fmt.Sprintf("%v/%v/%v", api.BaseURL(u.Config.API.UploadPrefix), u.Config.Cloud.CloudName, api.BuildPath(urlPath))
}

func getAssetType(requestParams interface{}) string {
	// FIXME: define interface or something to just access the field, and/or have a default value ("image") in the struct
	assetType := fmt.Sprintf("%v", reflect.ValueOf(requestParams).FieldByName("ResourceType"))
	if assetType == "" {
		assetType = api.Image.String()
	}

	return assetType
}

// randomPublicID generates a random public ID string, which is the first 16 characters of sha1 of uuid.
func randomPublicID() string {
	hash := sha1.New()
	hash.Write([]byte(uuid.NewString()))

	return hex.EncodeToString(hash.Sum(nil))[0:16]
}

// min returns minimum of two integers
func min(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
