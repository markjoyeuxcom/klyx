package appbridge

import (
	"errors"

	"github.com/moomora/klyx/internal/fleet"
)

// v0.15.1: a refused apply's fields and their managers cross as structure, so
// the YAML panel can open the owner an edit belongs in.

const (
	maxConflictFields = 32
	maxConflictPath   = 256
)

func conflictDTO(err error) *ApplyConflictDTO {
	var ce *fleet.ApplyConflictError
	if !errors.As(err, &ce) {
		return nil
	}
	d := &ApplyConflictDTO{Owner: ownerDTO(ce.Owner)}
	for i, f := range ce.Fields {
		if i == maxConflictFields {
			d.FieldsOmitted = len(ce.Fields) - maxConflictFields
			break
		}
		d.Fields = append(d.Fields, ConflictFieldDTO{Manager: clipText(f.Manager, maxConflictPath), Class: f.Class, Path: clipText(f.Path, maxConflictPath)})
	}
	return d
}

func ownerDTO(o *fleet.ConflictOwner) *ConflictOwnerDTO {
	if o == nil {
		return nil
	}
	return &ConflictOwnerDTO{Group: o.Group, Version: o.Version, Kind: o.Kind, Namespace: o.Namespace, Name: o.Name, Up: ownerDTO(o.Up)}
}

// clipText cuts s at max bytes on a rune boundary.
func clipText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}
