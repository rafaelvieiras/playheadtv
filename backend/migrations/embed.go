// Package migrations embeds the SQL migration files into the binary so a
// deployment never depends on the filesystem layout around it (important for
// the Docker image, which ships nothing but the compiled binary).
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
