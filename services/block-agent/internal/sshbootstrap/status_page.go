package sshbootstrap

import (
	"html"
	"strings"
)

const statusPageTemplate = `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <title>SSH Bootstrap</title>
</head>
<body>
  <main>
    <h1>SSH Bootstrap</h1>
    <dl>
      <dt>serviceName</dt>
      <dd id="service-name">ssh-bootstrap</dd>
      <dt>version</dt>
      <dd id="service-version">1.0.0</dd>
      <dt>nodeRole</dt>
      <dd id="node-role">{{nodeRole}}</dd>
      <dt>siteId</dt>
      <dd id="site-id">{{siteId}}</dd>
      <dt>blockId</dt>
      <dd id="block-id">{{blockId}}</dd>
      <dt>deviceId</dt>
      <dd id="device-id">{{deviceId}}</dd>
      <dt>certificateEndpoint</dt>
      <dd id="certificate-endpoint">POST /v1/ssh/cert</dd>
      <dt>certificateTtlSeconds</dt>
      <dd id="certificate-ttl-seconds">300</dd>
      <dt>currentEndpoint</dt>
      <dd id="current-endpoint">https://{{advertisedHost}}:9443</dd>
    </dl>
    <p id="credential-boundary">返回短期 SSH 证书，不返回私钥/密码。</p>
    <h2>request</h2>
    <pre id="request-command"><code>ssh-bootstrapctl request --endpoint https://{{advertisedHost}}:9443 --target {{nodeRole}} --site-id {{siteId}} --block-id {{blockId}} --device-id {{deviceId}} --profile &lt;release|debug&gt; --admin-kid &lt;ADMIN_KID&gt; --admin-key &lt;LOCAL_ADMIN_KEY&gt; --ca &lt;LOCAL_HTTPS_CA&gt; --server-name {{advertisedHost}} --output-dir &lt;LOCAL_SESSION_DIR&gt;</code></pre>
    <h2>connect</h2>
    <pre id="connect-command"><code>ssh-bootstrapctl connect --output-dir &lt;LOCAL_SESSION_DIR&gt;</code></pre>
  </main>
</body>
</html>
`

func renderStatusPage(config Config) string {
	return strings.NewReplacer(
		"{{nodeRole}}", html.EscapeString(config.TargetNode),
		"{{siteId}}", html.EscapeString(config.SiteID),
		"{{blockId}}", html.EscapeString(config.BlockID),
		"{{deviceId}}", html.EscapeString(config.DeviceID),
		"{{advertisedHost}}", html.EscapeString(config.AdvertisedHost),
	).Replace(statusPageTemplate)
}
