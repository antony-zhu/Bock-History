// strict-local-https-client performs one local HTTPS request for development
// verification. It deliberately has no plaintext or certificate-bypass mode.
package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type response struct {
	StatusCode  int    `json:"statusCode"`
	ContentType string `json:"contentType"`
	SetCookie   string `json:"setCookie"`
	Body        string `json:"body"`
}

func main() {
	rawURL := flag.String("url", "", "HTTPS URL to request")
	caPath := flag.String("ca", "", "public CA certificate PEM path")
	method := flag.String("method", "GET", "HTTP method")
	bodyPath := flag.String("body-file", "", "optional UTF-8 request body file")
	flag.Parse()

	if *rawURL == "" || *caPath == "" {
		fail("-url and -ca are required")
	}
	parsedURL, err := url.Parse(*rawURL)
	if err != nil || parsedURL.Scheme != "https" || parsedURL.Hostname() == "" {
		fail("-url must be an absolute https URL")
	}
	caPEM, err := os.ReadFile(*caPath)
	if err != nil {
		fail("read CA certificate: %v", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		fail("CA certificate contains no PEM certificates")
	}
	var body io.Reader
	if *bodyPath != "" {
		payload, err := os.ReadFile(*bodyPath)
		if err != nil {
			fail("read request body: %v", err)
		}
		body = bytes.NewReader(payload)
	}
	request, err := httpRequest(*method, parsedURL.String(), body)
	if err != nil {
		fail("build request: %v", err)
	}
	request.Header.Set("Accept", "application/json")
	if *bodyPath != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	client := &httpClient{
		rootCAs:    roots,
		serverName: parsedURL.Hostname(),
	}
	result, err := client.do(request)
	if err != nil {
		fail("strict HTTPS request: %v", err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fail("encode response: %v", err)
	}
}

type httpClient struct {
	rootCAs    *x509.CertPool
	serverName string
}

func (c *httpClient) do(request *http.Request) (response, error) {
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			RootCAs:    c.rootCAs,
			ServerName: c.serverName,
			MinVersion: tls.VersionTLS12,
		}},
	}
	httpResponse, err := client.Do(request)
	if err != nil {
		return response{}, err
	}
	defer httpResponse.Body.Close()
	body, err := io.ReadAll(httpResponse.Body)
	if err != nil {
		return response{}, err
	}
	return response{
		StatusCode:  httpResponse.StatusCode,
		ContentType: httpResponse.Header.Get("Content-Type"),
		SetCookie:   httpResponse.Header.Get("Set-Cookie"),
		Body:        string(body),
	}, nil
}

func httpRequest(method, rawURL string, body io.Reader) (*http.Request, error) {
	return http.NewRequest(method, rawURL, body)
}

func fail(format string, values ...any) {
	message := fmt.Sprintf(format, values...)
	message = strings.TrimSpace(message)
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}
