// Package api embeds the public OpenAPI contract and Swagger UI distribution.
package api

import "embed"

var Specification []byte

//go:embed swagger-ui/*
var SwaggerUI embed.FS
