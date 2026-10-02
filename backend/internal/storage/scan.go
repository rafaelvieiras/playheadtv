package storage

import (
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"unicode"
)

// toSnakeCase converts an exported Go field name into the snake_case column
// name it maps to, acronym-aware so "TenantID" -> "tenant_id" and
// "GuideArtURL" -> "guide_art_url" instead of "tenant_i_d"/"guide_art_u_r_l".
func toSnakeCase(name string) string {
	runes := []rune(name)
	var b strings.Builder
	for i, r := range runes {
		if unicode.IsUpper(r) && i > 0 {
			prev := runes[i-1]
			nextIsLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
			if unicode.IsLower(prev) || (unicode.IsUpper(prev) && nextIsLower) {
				b.WriteByte('_')
			}
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// scanRows fills dest from rows, which must be one of:
//   - *[]StructT: one row per element, columns matched to exported fields by
//     toSnakeCase(fieldName)
//   - *StructT: at most one row expected; returns sql.ErrNoRows if none was
//     found, mirroring (*sql.Row).Scan
//   - *[]T for a scalar T (string, int64, ...): one column expected per row
func scanRows(rows *sql.Rows, dest any) error {
	defer rows.Close()

	destVal := reflect.ValueOf(dest)
	if destVal.Kind() != reflect.Pointer || destVal.IsNil() {
		return fmt.Errorf("storage: scan dest must be a non-nil pointer, got %T", dest)
	}
	elemType := destVal.Elem().Type()

	if elemType.Kind() == reflect.Slice {
		sliceVal := reflect.MakeSlice(elemType, 0, 0)
		itemType := elemType.Elem()
		for rows.Next() {
			itemVal := reflect.New(itemType).Elem()
			if err := scanInto(rows, itemVal); err != nil {
				return err
			}
			sliceVal = reflect.Append(sliceVal, itemVal)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		destVal.Elem().Set(sliceVal)
		return nil
	}

	if elemType.Kind() != reflect.Struct {
		return fmt.Errorf("storage: unsupported scan dest %T", dest)
	}

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return err
		}
		return sql.ErrNoRows
	}
	if err := scanInto(rows, destVal.Elem()); err != nil {
		return err
	}
	return rows.Err()
}

// scanInto scans the current row into itemVal, a struct or a scalar.
func scanInto(rows *sql.Rows, itemVal reflect.Value) error {
	if itemVal.Kind() != reflect.Struct {
		return rows.Scan(itemVal.Addr().Interface())
	}

	cols, err := rows.Columns()
	if err != nil {
		return err
	}

	fieldByCol := make(map[string][]int, itemVal.NumField())
	collectFields(itemVal.Type(), nil, fieldByCol)

	targets := make([]any, len(cols))
	nullables := make(map[int]nullableScan, len(cols))
	for i, col := range cols {
		fieldIdx, ok := fieldByCol[col]
		if !ok {
			var discard any
			targets[i] = &discard
			continue
		}
		fieldVal := itemVal.FieldByIndex(fieldIdx)
		target, nullable := scanTargetFor(fieldVal)
		targets[i] = target
		if nullable != nil {
			nullables[i] = nullable
		}
	}

	if err := rows.Scan(targets...); err != nil {
		return err
	}

	for i, fill := range nullables {
		fill(itemVal.FieldByIndex(fieldByCol[cols[i]]))
	}
	return nil
}

// collectFields records, for every exported field of t, the index path to it
// keyed by its snake_case column name. An embedded (anonymous) struct is
// flattened into its parent, mirroring how encoding/json treats it, so a
// shared group of columns can be declared once and embedded.
func collectFields(t reflect.Type, prefix []int, out map[string][]int) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		path := append(append([]int(nil), prefix...), i)
		if f.Anonymous && f.Type.Kind() == reflect.Struct {
			collectFields(f.Type, path, out)
			continue
		}
		out[toSnakeCase(f.Name)] = path
	}
}

// nullableScan assigns the value collected in a sql.Null* scan target back
// onto a pointer-typed struct field once Scan has run.
type nullableScan func(fieldVal reflect.Value)

// scanTargetFor returns the address Scan should write into for fieldVal, and
// (for pointer-typed fields) a follow-up closure that copies the scanned
// value back onto the field.
func scanTargetFor(fieldVal reflect.Value) (target any, nullable nullableScan) {
	switch fieldVal.Interface().(type) {
	case *string:
		var ns sql.NullString
		return &ns, func(f reflect.Value) {
			if ns.Valid {
				f.Set(reflect.ValueOf(&ns.String))
			} else {
				f.Set(reflect.Zero(f.Type()))
			}
		}
	case *int64:
		var ni sql.NullInt64
		return &ni, func(f reflect.Value) {
			if ni.Valid {
				f.Set(reflect.ValueOf(&ni.Int64))
			} else {
				f.Set(reflect.Zero(f.Type()))
			}
		}
	case *int:
		var ni sql.NullInt64
		return &ni, func(f reflect.Value) {
			if ni.Valid {
				v := int(ni.Int64)
				f.Set(reflect.ValueOf(&v))
			} else {
				f.Set(reflect.Zero(f.Type()))
			}
		}
	case *float64:
		var nf sql.NullFloat64
		return &nf, func(f reflect.Value) {
			if nf.Valid {
				f.Set(reflect.ValueOf(&nf.Float64))
			} else {
				f.Set(reflect.Zero(f.Type()))
			}
		}
	case *bool:
		var nb sql.NullBool
		return &nb, func(f reflect.Value) {
			if nb.Valid {
				f.Set(reflect.ValueOf(&nb.Bool))
			} else {
				f.Set(reflect.Zero(f.Type()))
			}
		}
	}
	return fieldVal.Addr().Interface(), nil
}
