// Copyright NetApp Inc, 2021 All rights reserved

package rest

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/netapp/harvest/v2/pkg/auth"
	"github.com/netapp/harvest/v2/pkg/conf"
	"github.com/netapp/harvest/v2/pkg/errs"
	"github.com/netapp/harvest/v2/pkg/logging"
	"github.com/netapp/harvest/v2/pkg/requests"
	"github.com/netapp/harvest/v2/pkg/tree/node"
	"github.com/netapp/harvest/v2/pkg/util"
	"github.com/tidwall/gjson"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"strings"
	"time"
)

const (
	// DefaultTimeout should be > than ONTAP's default REST timeout, which is 15 seconds for GET requests
	DefaultTimeout = "30s"
	// DefaultDialerTimeout limits the time spent establishing a TCP connection
	DefaultDialerTimeout = 10 * time.Second
	Message              = "message"
	Code                 = "code"
	Target               = "target"
)

type Client struct {
	client  *http.Client
	request *http.Request
	buffer  *bytes.Buffer
	Logger  *logging.Logger
	baseURL string
	cluster Cluster
	Timeout time.Duration
	logRest bool // used to log Rest request/response
	auth    *auth.Credentials
}

type Cluster struct {
	Name    string
	Info    string
	UUID    string
	Version [3]int
}

func New(poller *conf.Poller, timeout time.Duration, auth *auth.Credentials) (*Client, error) {
	var (
		client     Client
		httpclient *http.Client
		transport  *http.Transport
		addr       string
		url        string
		err        error
	)

	client = Client{
		auth: auth,
	}
	client.Logger = logging.Get().SubLogger("REST", "Client")

	if addr = poller.Addr; addr == "" {
		return nil, errs.New(errs.ErrMissingParam, "addr")
	}

	if poller.IsKfs {
		url = "https://" + addr + ":8443/"
	} else {
		url = "https://" + addr + "/"
	}
	client.baseURL = url
	client.Timeout = timeout

	transport, err = auth.Transport(nil)
	if err != nil {
		return nil, err
	}
	transport.DialContext = (&net.Dialer{Timeout: DefaultDialerTimeout}).DialContext
	httpclient = &http.Client{Transport: transport, Timeout: timeout}
	client.client = httpclient

	return &client, nil
}

func (c *Client) TraceLogSet(collectorName string, config *node.Node) {
	// check for log sets and enable Rest request logging if collectorName is in the set
	if llogs := config.GetChildS("log"); llogs != nil {
		for _, log := range llogs.GetAllChildContentS() {
			if strings.EqualFold(log, collectorName) {
				c.logRest = true
			}
		}
	}
}

func (c *Client) printRequestAndResponse(req string, response []byte) {
	if c.logRest {
		c.Logger.Info().
			Str("Request", req).
			Bytes("Response", response).
			Send()
	}
}

// GetRest makes a REST request to the cluster and returns a json response as a []byte
func (c *Client) GetRest(request string) ([]byte, error) {
	var err error
	if strings.Index(request, "/") == 0 {
		request = request[1:]
	}
	request, err = util.EncodeURL(request)
	if err != nil {
		return nil, err
	}
	u := c.baseURL + request
	c.request, err = requests.New("GET", u, nil)
	if err != nil {
		return nil, err
	}
	c.request.Header.Set("accept", "application/json")
	pollerAuth, err := c.auth.GetPollerAuth()
	if err != nil {
		return nil, err
	}
	if pollerAuth.Username != "" {
		c.request.SetBasicAuth(pollerAuth.Username, pollerAuth.Password)
	}
	// ensure that we can change body dynamically
	c.request.GetBody = func() (io.ReadCloser, error) {
		r := bytes.NewReader(c.buffer.Bytes())
		return io.NopCloser(r), nil
	}
	if err != nil {
		return nil, err
	}

	result, err := c.invokeWithAuthRetry()
	return result, err
}

func (c *Client) invokeWithAuthRetry() ([]byte, error) {
	var (
		body []byte
		err  error
	)

	doInvoke := func() ([]byte, error) {
		var (
			response  *http.Response
			innerBody []byte
			innerErr  error
		)

		if c.request.Body != nil {
			//goland:noinspection GoUnhandledErrorResult
			defer response.Body.Close()
		}
		if c.buffer != nil {
			defer c.buffer.Reset()
		}

		restReq := c.request.URL.String()

		// send request to server
		if response, innerErr = c.client.Do(c.request); innerErr != nil {
			return nil, fmt.Errorf("connection error %w", innerErr)
		}
		//goland:noinspection GoUnhandledErrorResult
		defer response.Body.Close()
		innerBody, innerErr = io.ReadAll(response.Body)
		if innerErr != nil {
			return nil, errs.Rest(response.StatusCode, innerErr.Error(), 0, "")
		}

		if response.StatusCode != http.StatusOK {

			if response.StatusCode == http.StatusUnauthorized {
				return nil, errs.New(errs.ErrAuthFailed, response.Status)
			}

			result := gjson.GetBytes(innerBody, "error")

			if response.StatusCode == http.StatusForbidden {
				message := result.Get(Message).String()
				return nil, errs.New(errs.ErrPermissionDenied, message)
			}

			if result.Exists() {
				message := result.Get(Message).String()
				code := result.Get(Code).Int()
				target := result.Get(Target).String()
				return nil, errs.Rest(response.StatusCode, message, code, target)
			}
			return nil, errs.Rest(response.StatusCode, "", 0, "")
		}

		defer c.printRequestAndResponse(restReq, innerBody)

		return innerBody, nil
	}

	body, err = doInvoke()

	if err != nil {
		var he errs.HarvestError
		if errors.As(err, &he) {
			// If this is an auth failure and the client is using a credential script,
			// expire the current credentials, call the script again, update the client's password,
			// and try again
			if errors.Is(he, errs.ErrAuthFailed) {
				pollerAuth, err2 := c.auth.GetPollerAuth()
				if err2 != nil {
					return nil, err2
				}
				if pollerAuth.HasCredentialScript {
					c.auth.Expire()
					pollerAuth2, err2 := c.auth.GetPollerAuth()
					if err2 != nil {
						return nil, err2
					}
					c.request.SetBasicAuth(pollerAuth2.Username, pollerAuth2.Password)
					return doInvoke()
				}
			}
		}
	}
	return body, err
}

func downloadSwagger(poller *conf.Poller, path string, url string, verbose bool) (int64, error) {
	out, err := os.Create(path)
	if err != nil {
		return 0, fmt.Errorf("unable to create %s to save swagger.yaml", path)
	}
	defer func(out *os.File) { _ = out.Close() }(out)
	request, err := requests.New("GET", url, nil)
	if err != nil {
		return 0, err
	}

	timeout, _ := time.ParseDuration(DefaultTimeout)
	credentials := auth.NewCredentials(poller, logging.Get())
	transport, err := credentials.Transport(request)
	if err != nil {
		return 0, err
	}
	httpclient := &http.Client{Transport: transport, Timeout: timeout}

	if verbose {
		requestOut, _ := httputil.DumpRequestOut(request, false)
		fmt.Printf("REQUEST: %s\n%s\n", url, requestOut)
	}
	response, err := httpclient.Do(request)
	if err != nil {
		return 0, err
	}
	//goland:noinspection GoUnhandledErrorResult
	defer response.Body.Close()

	if verbose {
		debugResp, _ := httputil.DumpResponse(response, false)
		fmt.Printf("RESPONSE: \n%s", debugResp)
	}
	if response.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("error making request. server response statusCode=[%d]", response.StatusCode)
	}
	n, err := io.Copy(out, response.Body)
	if err != nil {
		return 0, fmt.Errorf("error while downloading %s err=%w", url, err)
	}
	return n, nil
}

func (c *Client) Init(retries int) error {

	var (
		err     error
		content []byte
		i       int
	)

	for i = 0; i < retries; i++ {

		content, err = c.GetRest(BuildHref("cluster", "*", nil, "", "", "", "", ""))
		if err != nil {
			if errors.Is(err, errs.ErrPermissionDenied) {
				return err
			}
			continue
		}

		results := gjson.ParseBytes(content)
		c.cluster.Name = results.Get("name").String()
		c.cluster.UUID = results.Get("uuid").String()
		c.cluster.Info = results.Get("version.full").String()
		c.cluster.Version[0] = int(results.Get("version.generation").Int())
		c.cluster.Version[1] = int(results.Get("version.major").Int())
		c.cluster.Version[2] = int(results.Get("version.minor").Int())
		return nil
	}
	return err
}

func BuildHref(apiPath string, fields string, field []string, queryFields string, queryValue string, maxRecords string, returnTimeout string, endpoint string) string {
	href := strings.Builder{}
	if endpoint == "" {
		href.WriteString("api/")
		href.WriteString(apiPath)
	} else {
		href.WriteString(endpoint)
	}
	href.WriteString("?return_records=true")
	addArg(&href, "&fields=", fields)
	for _, f := range field {
		addArg(&href, "&", f)
	}
	addArg(&href, "&query_fields=", queryFields)
	addArg(&href, "&query=", queryValue)
	addArg(&href, "&max_records=", maxRecords)
	addArg(&href, "&return_timeout=", returnTimeout)
	return href.String()
}

func addArg(href *strings.Builder, field string, value string) {
	if value == "" {
		return
	}
	href.WriteString(field)
	href.WriteString(value)
}

func (c *Client) Cluster() Cluster {
	return c.cluster
}

func (cl Cluster) GetVersion() string {
	ver := cl.Version
	return fmt.Sprintf("%d.%d.%d", ver[0], ver[1], ver[2])

}
