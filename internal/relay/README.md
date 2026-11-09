# ClipSend notification relay

The relay sends fixed, data-free APNs alerts. Keep the `.p8` key on this service
and provision scoped random bearer tokens out of band. Public enrollment is a
release gate; this service has no enrollment endpoint. Run behind a TLS
terminating proxy and keep the process bound to loopback. Do not publish its
port directly. Only private HTTPS relay addresses can be used by the client.

Example `/etc/clipsend-relay/config.json` (replace every placeholder):

```json
{
  "keyID": "ABCDEFGHIJ",
  "teamID": "KLMNOPQRST",
  "keyPath": "/etc/clipsend-relay/AuthKey_PLACEHOLDER.p8",
  "listen": "127.0.0.1:8787",
  "requestsPerMinute": 30,
  "allowedBundleIDs": ["com.example.clipsend"],
  "credentials": [
    {"token": "REPLACE_WITH_RANDOM_32_BYTE_OR_LONGER_BEARER", "bundleIDs": ["com.example.clipsend"], "computerIDs": ["OPERATOR_PROVISIONED_COMPUTER_ID"]}
  ]
}
```

Make the config and key readable only by the service account (mode 0600),
and store client `relayURL` and `relayToken` in the private ClipSend config.
The key path must be absolute and the key is never copied to clients. The
service accepts `POST /v1/notify`, enforces the app allowlist and per-token
rate limit, and returns 202 only after Apple accepts the push. Apple acceptance
does not mean the receiving device fetched text.

Client API: `(&relay.Client{RelayURL: url, Token: token}).Notify(ctx, request)`.
The service handler is exposed as `(*relay.Service).Handler()` for embedding
and tests. The default service listener is `127.0.0.1:8787`.
