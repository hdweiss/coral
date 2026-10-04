package k8s

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/duration"
	"k8s.io/client-go/util/jsonpath"
)

// printerColumns turns a custom resource's additionalPrinterColumns into
// table columns: NAME, the printer columns, and AGE unless a printer column
// already shows the creation time. Columns with a priority above zero are
// hidden first, as kubectl get hides them without -o wide.
func printerColumns(pcs []PrinterColumn) []Column {
	cols := []Column{colName}
	hasAge := false
	for _, pc := range pcs {
		if strings.TrimSpace(pc.JSONPath) == ".metadata.creationTimestamp" {
			hasAge = true
		}
		cols = append(cols, printerColumn(pc))
	}
	if !hasAge {
		cols = append(cols, colAge)
	}
	return cols
}

func printerColumn(pc PrinterColumn) Column {
	jp := jsonpath.New(pc.Name).AllowMissingKeys(true)
	if err := jp.Parse("{" + pc.JSONPath + "}"); err != nil {
		jp = nil
	}
	// value is the first result, like the API server's table conversion.
	value := func(u *unstructured.Unstructured) (any, bool) {
		if jp == nil {
			return nil, false
		}
		results, err := jp.FindResults(u.Object)
		if err != nil || len(results) == 0 || len(results[0]) == 0 {
			return nil, false
		}
		return results[0][0].Interface(), true
	}
	c := Column{
		Name: strings.ToUpper(pc.Name),
		Drop: int(pc.Priority),
		Value: func(u *unstructured.Unstructured) string {
			v, ok := value(u)
			if !ok {
				return ""
			}
			return formatCell(v)
		},
	}
	switch pc.Type {
	case "date":
		at := func(u *unstructured.Unstructured) (time.Time, bool) {
			v, ok := value(u)
			s, _ := v.(string)
			t, err := time.Parse(time.RFC3339, s)
			return t, ok && err == nil
		}
		c.Value = func(u *unstructured.Unstructured) string {
			t, ok := at(u)
			switch {
			case !ok:
				return ""
			case t.After(time.Now()): // e.g. a certificate's expiry
				return "in " + duration.HumanDuration(time.Until(t))
			}
			return duration.HumanDuration(time.Since(t))
		}
		// Like AGE: ascending lists the youngest first; missing dates last.
		c.Sort = func(u *unstructured.Unstructured) any {
			if t, ok := at(u); ok {
				return -float64(t.Unix())
			}
			return float64(1 << 62)
		}
	case "integer", "number":
		c.Sort = func(u *unstructured.Unstructured) any {
			v, _ := value(u)
			switch n := v.(type) {
			case int64:
				return float64(n)
			case float64:
				return n
			case int:
				return float64(n)
			}
			return float64(-1 << 62) // missing first
		}
	}
	return c
}

func formatCell(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case map[string]any, []any:
		b, err := json.Marshal(t)
		if err != nil {
			return fmt.Sprint(t)
		}
		return string(b)
	}
	return fmt.Sprint(v)
}

// crdColumns are the columns of the CRDs list. GROUP is hidden first, then
// VERSIONS.
func crdColumns() []Column {
	return []Column{colName,
		col("GROUP", field("spec", "group")).dropFirst(2),
		col("KIND", field("spec", "names", "kind")),
		col("SCOPE", field("spec", "scope")),
		col("VERSIONS", func(u *unstructured.Unstructured) string { return strings.Join(CRDVersions(u), ",") }).dropFirst(1),
		colAge}
}
