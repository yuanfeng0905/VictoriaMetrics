package datasource

import (
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/VictoriaMetrics/VictoriaMetrics/app/vmalert/vmalertutil"
	"github.com/VictoriaMetrics/VictoriaMetrics/lib/flagutil"
	"github.com/VictoriaMetrics/VictoriaMetrics/lib/httputil"
	"github.com/VictoriaMetrics/VictoriaMetrics/lib/promauth"
)

var (
	addr = flag.String("datasource.url", "", "Datasource compatible with Prometheus HTTP API. It can be single node VictoriaMetrics or vmselect endpoint. Required parameter. "+
		"Supports address in the form of IP address with a port (e.g., http://127.0.0.1:8428) or DNS SRV record. "+
		"See also -remoteRead.disablePathAppend and -datasource.showURL")
	vlogsAddr = flag.String("vlogs.datasource.url", "", "It can be single node VictoriaMetrics-logs or vlselect endpoint. Required parameter. "+
		"Supports address in the form of IP address with a port (e.g., http://127.0.0.1:8428) or DNS SRV record. "+
		"See also -remoteRead.disablePathAppend and -datasource.showURL")
	appendTypePrefix  = flag.Bool("datasource.appendTypePrefix", false, "Whether to add type prefix to -datasource.url based on the query type. Set to true if sending different query types to the vmselect URL.")
	showDatasourceURL = flag.Bool("datasource.showURL", false, "Whether to avoid stripping sensitive information such as auth headers or passwords from URLs in log messages or UI and exported metrics. "+
		"It is hidden by default, since it can contain sensitive info such as auth key")

	headers = flag.String("datasource.headers", "", "Optional HTTP extraHeaders to send with each request to the corresponding -datasource.url. "+
		"For example, -datasource.headers='My-Auth:foobar' would send 'My-Auth: foobar' HTTP header with every request to the corresponding -datasource.url. "+
		"Multiple headers must be delimited by '^^': -datasource.headers='header1:value1^^header2:value2'")

	basicAuthUsername     = flag.String("datasource.basicAuth.username", "", "Optional basic auth username for -datasource.url")
	basicAuthPassword     = flag.String("datasource.basicAuth.password", "", "Optional basic auth password for -datasource.url")
	basicAuthPasswordFile = flag.String("datasource.basicAuth.passwordFile", "", "Optional path to basic auth password to use for -datasource.url")

	bearerToken     = flag.String("datasource.bearerToken", "", "Optional bearer auth token to use for -datasource.url.")
	bearerTokenFile = flag.String("datasource.bearerTokenFile", "", "Optional path to bearer token file to use for -datasource.url.")

	tlsInsecureSkipVerify = flag.Bool("datasource.tlsInsecureSkipVerify", false, "Whether to skip tls verification when connecting to -datasource.url")
	tlsCertFile           = flag.String("datasource.tlsCertFile", "", "Optional path to client-side TLS certificate file to use when connecting to -datasource.url")
	tlsKeyFile            = flag.String("datasource.tlsKeyFile", "", "Optional path to client-side TLS certificate key to use when connecting to -datasource.url")
	tlsCAFile             = flag.String("datasource.tlsCAFile", "", `Optional path to TLS CA file to use for verifying connections to -datasource.url. By default, system CA is used`)
	tlsServerName         = flag.String("datasource.tlsServerName", "", `Optional TLS server name to use for connections to -datasource.url. By default, the server name from -datasource.url is used`)

	oauth2ClientID         = flag.String("datasource.oauth2.clientID", "", "Optional OAuth2 clientID to use for -datasource.url")
	oauth2ClientSecret     = flag.String("datasource.oauth2.clientSecret", "", "Optional OAuth2 clientSecret to use for -datasource.url")
	oauth2ClientSecretFile = flag.String("datasource.oauth2.clientSecretFile", "", "Optional OAuth2 clientSecretFile to use for -datasource.url")
	oauth2EndpointParams   = flag.String("datasource.oauth2.endpointParams", "", "Optional OAuth2 endpoint parameters to use for -datasource.url . "+
		`The endpoint parameters must be set in JSON format: {"param1":"value1",...,"paramN":"valueN"}`)
	oauth2TokenURL = flag.String("datasource.oauth2.tokenUrl", "", "Optional OAuth2 tokenURL to use for -datasource.url")
	oauth2Scopes   = flag.String("datasource.oauth2.scopes", "", "Optional OAuth2 scopes to use for -datasource.url. Scopes must be delimited by ';'")

	queryStep = flag.Duration("datasource.queryStep", 5*time.Minute, "How far a value can fallback to when evaluating queries to the configured -datasource.url and -remoteRead.url. Only valid for prometheus datasource. "+
		"For example, if -datasource.queryStep=15s then param \"step\" with value \"15s\" will be added to every query. "+
		"If set to 0, rule's evaluation interval will be used instead.")
	maxIdleConnections    = flag.Int("datasource.maxIdleConnections", 100, `Defines the number of idle (keep-alive connections) to each configured datasource. Consider setting this value equal to the value: groups_total * group.concurrency. Too low a value may result in a high number of sockets in TIME_WAIT state.`)
	idleConnectionTimeout = flag.Duration("datasource.idleConnTimeout", 50*time.Second, `Defines a duration for idle (keep-alive connections) to exist. Consider setting this value less than "-http.idleConnTimeout". It must prevent possible "write: broken pipe" and "read: connection reset by peer" errors.`)
	disableKeepAlive      = flag.Bool("datasource.disableKeepAlive", false, `Whether to disable long-lived connections to the datasource. `+
		`If true, disables HTTP keep-alive and will only use the connection to the server for a single HTTP request.`)
	roundDigits = flag.Int("datasource.roundDigits", 0, `Adds "round_digits" GET param to datasource requests which limits the number of digits after the decimal point in response values. `+
		`Only valid for VictoriaMetrics as the datasource.`)
)

// InitSecretFlags must be called after flag.Parse and before any logging
func InitSecretFlags() {
	if !*showDatasourceURL {
		flagutil.RegisterSecretFlag("datasource.url")
	}
}

// ShowDatasourceURL whether to show -datasource.url with sensitive information
func ShowDatasourceURL() bool {
	return *showDatasourceURL
}

// Param represents an HTTP GET param
type Param struct {
	Key, Value string
}

// Init creates a Querier from provided flag values.
// Provided extraParams will be added as GET params for
// each request.
func Init(extraParams url.Values) (QuerierBuilder, error) {
	if err := httputil.CheckURL(*addr); err != nil {
		return nil, fmt.Errorf("invalid -datasource.url: %w", err)
	}
	tr, err := promauth.NewTLSTransport(*tlsCertFile, *tlsKeyFile, *tlsCAFile, *tlsServerName, *tlsInsecureSkipVerify, "vmalert_datasource")
	if err != nil {
		return nil, fmt.Errorf("failed to create transport for -datasource.url=%q: %w", *addr, err)
	}
	tr.DisableKeepAlives = *disableKeepAlive
	tr.MaxIdleConnsPerHost = *maxIdleConnections
	if tr.MaxIdleConns != 0 && tr.MaxIdleConns < tr.MaxIdleConnsPerHost {
		tr.MaxIdleConns = tr.MaxIdleConnsPerHost
	}
	tr.IdleConnTimeout = *idleConnectionTimeout

	if extraParams == nil {
		extraParams = url.Values{}
	}
	if *roundDigits > 0 {
		extraParams.Set("round_digits", fmt.Sprintf("%d", *roundDigits))
	}

	endpointParams, err := flagutil.ParseJSONMap(*oauth2EndpointParams)
	if err != nil {
		return nil, fmt.Errorf("cannot parse JSON for -datasource.oauth2.endpointParams=%s: %w", *oauth2EndpointParams, err)
	}
	authCfg, err := vmalertutil.AuthConfig(
		vmalertutil.WithBasicAuth(*basicAuthUsername, *basicAuthPassword, *basicAuthPasswordFile),
		vmalertutil.WithBearer(*bearerToken, *bearerTokenFile),
		vmalertutil.WithOAuth(*oauth2ClientID, *oauth2ClientSecret, *oauth2ClientSecretFile, *oauth2TokenURL, *oauth2Scopes, endpointParams),
		vmalertutil.WithHeaders(*headers))
	if err != nil {
		return nil, fmt.Errorf("failed to configure auth: %w", err)
	}
	_, err = authCfg.GetAuthHeader()
	if err != nil {
		return nil, fmt.Errorf("failed to set request auth header to datasource %q: %w", *addr, err)
	}

	return &Client{
		c:                &http.Client{Transport: tr},
		authCfg:          authCfg,
		datasourceURL:    strings.TrimSuffix(*addr, "/"),
		appendTypePrefix: *appendTypePrefix,
		queryStep:        *queryStep,
		extraParams:      extraParams,
	}, nil
}
