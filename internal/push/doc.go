// Package push stores native APNs pairings and sends a generic clipboard-ready
// alert. It never receives clipboard contents or a destination URL.
//
// Setup is intentionally explicit: create a private push.json in configDir with
// keyID and teamID (Apple's ten-character uppercase identifiers), and keyPath
// (an absolute path to a private PKCS#8 EC P-256 .p8 PEM file). Keep push.json
// and the key accessible only to their owner. Pair does not require this setup.
//
// Pair accepts exactly the native version-1 JSON fields version, deviceName,
// deviceToken, bundleID, and environment. Tokens are nonempty, even-length hex,
// at most 4096 characters; their length is otherwise unrestricted. Environment
// must be sandbox or production. Aliases are case-insensitive, printable UTF-8
// names up to 128 bytes, with no surrounding whitespace or path separators.
// devices.json is a JSON object mapping aliases to these pairings. Unknown or
// duplicate JSON fields and invalid existing entries are errors, not discarded.
//
// Pair and Unpair serialize updates within the process and replace devices.json
// atomically with mode 0600 in a directory with mode 0700. Callers must serialize
// simultaneous pairing changes across processes. Unpair is idempotent.
//
// Notify returns false, nil when push.json is absent or the alias is unpaired.
// Otherwise errors describe configuration or request failures without including
// device metadata, tokens, private paths, JWTs, or untrusted APNs response text.
// A true result means APNs accepted the alert, not that the device received it
// or that LocalSend opened. Each call has a 15-second request timeout and never
// replays a notification. Caller cancellation is preserved via errors.Is.
package push
