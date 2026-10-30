package api

import (
	"encoding/json"
	"reflect"
	"strings"
)

// A SENT COLLECTION REPLACES THE STORED ONE.
//
// Several handlers decode the request body OVER the stored row, so that a client
// sending only the field it changed does not blank the rest: PUT
// /destinations/{id}, /renditions/{id}, /sources/{id} and /settings all work
// that way. For scalars and objects that is exactly right. For arrays and maps
// encoding/json does something else:
//
//   - An array decodes into the stored slice ELEMENT BY ELEMENT, by index, into
//     the stored element's memory. A key the client left out of an element keeps
//     whatever the stored element at that INDEX held. The routing profile's
//     cells and tracks carry `invert` and `muted` as omitempty bools, which a
//     client sends as absent when false -- so un-inverting a cell, or saving a
//     re-sorted list, left `true` behind on whichever element now sat at that
//     index. The operator un-inverted a channel, got 200, and the stored mix
//     still inverted one; or re-sorted the cells and moved an inversion onto a
//     channel nobody touched.
//   - A map MERGES: a key the client removed keeps its stored value.
//
// Neither is ever what a client sending a whole array or map means. So before
// the decode, every slice, array and map field whose key the body carries is
// zeroed, at every depth the body reaches, and the decode then fills it from
// the body alone. A field the body does not mention is untouched, which keeps
// the partial-payload property every one of these handlers depends on. Explicit
// null counts as sent and clears, as it always did.
//
// Done HERE, ahead of the strict decode, rather than with an UnmarshalJSON on
// each type, for two reasons this codebase has already paid for:
//
//   - A custom UnmarshalJSON silently switches off DisallowUnknownFields for
//     everything under it (see db.CheckAutomodFields, which exists to put that
//     back for automod alone). Here the one strict decode below still runs.
//   - A method on an EMBEDDED struct (db.AnnouncementSet inside the Facebook
//     settings) is promoted onto the outer type and swallows its whole decode.
//
// A type that has its own UnmarshalJSON owns its own decode semantics and is
// left to it (db.AutomodSettings).
func resetSentCollections(body []byte, v any) {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return
	}
	resetValue(body, rv.Elem())
}

var unmarshalerType = reflect.TypeFor[json.Unmarshaler]()

// resetValue walks one JSON value against the Go value it will decode into.
func resetValue(raw []byte, v reflect.Value) {
	// encoding/json decodes into the value a non-nil pointer already points AT,
	// so that is the value whose collections must be cleared. A nil pointer is
	// allocated fresh by the decode and has nothing stale in it.
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct || ownsDecode(v) {
		return
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil || obj == nil {
		// Not an object (or null): the decode replaces or rejects it whole.
		return
	}
	resetFields(obj, v)
}

// ownsDecode reports whether v has its own UnmarshalJSON.
func ownsDecode(v reflect.Value) bool {
	return v.CanAddr() && v.Addr().Type().Implements(unmarshalerType)
}

// resetFields clears each collection field of struct v whose key obj carries,
// and walks into each struct field whose key it carries.
func resetFields(obj map[string]json.RawMessage, v reflect.Value) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		fv := v.Field(i)

		// An untagged embedded struct's fields are read from THIS object, as
		// encoding/json flattens them.
		if f.Anonymous && name == "" {
			ft := f.Type
			if ft.Kind() == reflect.Pointer {
				if fv.IsNil() {
					continue
				}
				fv, ft = fv.Elem(), ft.Elem()
			}
			if ft.Kind() == reflect.Struct && !ownsDecode(fv) {
				resetFields(obj, fv)
				continue
			}
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		raw, ok := lookupKey(obj, name)
		if !ok {
			continue
		}
		switch fv.Kind() {
		case reflect.Slice, reflect.Map, reflect.Array:
			if fv.CanSet() {
				fv.SetZero()
			}
		case reflect.Struct, reflect.Pointer:
			resetValue(raw, fv)
		}
	}
}

// lookupKey finds a field's key the way encoding/json matches it: exactly, or
// failing that case-insensitively.
func lookupKey(obj map[string]json.RawMessage, name string) (json.RawMessage, bool) {
	if raw, ok := obj[name]; ok {
		return raw, true
	}
	for k, raw := range obj {
		if strings.EqualFold(k, name) {
			return raw, true
		}
	}
	return nil, false
}
