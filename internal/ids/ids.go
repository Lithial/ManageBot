// Package ids generates ULIDs for use as primary keys across the wrap data model.
package ids

import (
	"crypto/rand"
	"time"

	"github.com/oklog/ulid/v2"
)

func New() string {
	return ulid.MustNew(ulid.Timestamp(time.Now()), rand.Reader).String()
}
