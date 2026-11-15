// Package certs holds the certificate authority used to verify the Schema databases
package certs

import _ "embed"

// SchemaCA is the Schema certificate authority (public) in PEM format
//
//go:embed schema-ca.crt
var SchemaCA []byte
