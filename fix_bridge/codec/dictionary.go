// SPDX-License-Identifier: MIT

package codec

import (
	"embed"
	"encoding/xml"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed spec/FIX44.xml spec/fxp.xml
var dictionaries embed.FS

type xmlNode struct {
	Kind     xml.Name
	Name     string    `xml:"name,attr"`
	Required string    `xml:"required,attr"`
	Children []xmlNode `xml:",any"`
}

// UnmarshalXML retains element kinds as well as their ordered children.
func (n *xmlNode) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	type plain xmlNode
	var v plain
	if err := d.DecodeElement(&v, &start); err != nil {
		return err
	}
	*n = xmlNode(v)
	n.Kind = start.Name
	return nil
}

type fieldDef struct {
	Number int    `xml:"number,attr"`
	Name   string `xml:"name,attr"`
	Type   string `xml:"type,attr"`
	Values []struct {
		Enum string `xml:"enum,attr"`
	} `xml:"value"`
}
type messageDef struct {
	Type  string    `xml:"msgtype,attr"`
	Nodes []xmlNode `xml:",any"`
}
type dictionaryXML struct {
	Header struct {
		Nodes []xmlNode `xml:",any"`
	} `xml:"header"`
	Trailer struct {
		Nodes []xmlNode `xml:",any"`
	} `xml:"trailer"`
	Fields     []fieldDef   `xml:"fields>field"`
	Messages   []messageDef `xml:"messages>message"`
	Components []struct {
		Name  string    `xml:"name,attr"`
		Nodes []xmlNode `xml:",any"`
	} `xml:"components>component"`
}
type rule struct {
	tag       int
	required  bool
	children  []*rule
	component bool
}
type Dictionary struct {
	fields          map[int]fieldDef
	byName          map[string]int
	messages        map[string][]*rule
	header, trailer []*rule
}

// DefaultDictionary is immutable after startup; all sessions share it.
var DefaultDictionary = loadDictionary()

func loadDictionary() *Dictionary {
	var source dictionaryXML
	raw, err := dictionaries.ReadFile("spec/FIX44.xml")
	if err == nil {
		err = xml.Unmarshal(raw, &source)
	}
	if err != nil {
		panic(err)
	}
	var profile struct {
		Fields   []fieldDef   `xml:"fields>field"`
		Messages []messageDef `xml:"messages>message"`
		Required []struct {
			Component string `xml:"component,attr"`
			Field     string `xml:"field,attr"`
		} `xml:"required"`
	}
	raw, err = dictionaries.ReadFile("spec/fxp.xml")
	if err == nil {
		err = xml.Unmarshal(raw, &profile)
	}
	if err != nil {
		panic(err)
	}
	d := &Dictionary{fields: map[int]fieldDef{}, byName: map[string]int{}, messages: map[string][]*rule{}}
	for _, f := range append(source.Fields, profile.Fields...) {
		d.fields[f.Number] = f
		d.byName[f.Name] = f.Number
	}
	components := map[string][]xmlNode{}
	for _, c := range source.Components {
		for i := range c.Nodes {
			for _, r := range profile.Required {
				if c.Name == r.Component && c.Nodes[i].Name == r.Field {
					c.Nodes[i].Required = "Y"
				}
			}
		}
		components[c.Name] = c.Nodes
	}
	var compile func([]xmlNode) []*rule
	compile = func(nodes []xmlNode) []*rule {
		var out []*rule
		for _, n := range nodes {
			r := &rule{required: n.Required == "Y"}
			if n.Kind.Local == "component" {
				r.component = true
				r.children = compile(components[n.Name])
			} else {
				r.tag = d.byName[n.Name]
				if r.tag == 0 {
					panic("unknown dictionary field: " + n.Name)
				}
				if n.Kind.Local == "group" {
					r.children = compile(n.Children)
				}
			}
			out = append(out, r)
		}
		return out
	}
	for _, m := range source.Messages {
		for _, p := range profile.Messages {
			if p.Type == m.Type {
				m.Nodes = append(m.Nodes, p.Nodes...)
			}
		}
		d.messages[m.Type] = compile(m.Nodes)
	}
	d.header = compile(source.Header.Nodes)
	d.trailer = compile(source.Trailer.Nodes)
	return d
}

type ValidationError struct {
	Tag          int
	Reason, Text string
}

func (e *ValidationError) Error() string { return e.Text }
func (d *Dictionary) failure(tag int, reason, text string) *ValidationError {
	return &ValidationError{tag, reason, fmt.Sprintf("%s: %s (%d)", text, d.fields[tag].Name, tag)}
}

// flatten expands components but leaves repeating groups as ordered subscopes.
func flatten(rules []*rule) []*rule {
	var out []*rule
	for _, r := range rules {
		if r.component {
			out = append(out, flatten(r.children)...)
		} else {
			out = append(out, r)
		}
	}
	return out
}
func present(r *rule, fields map[int]bool) bool {
	if !r.component {
		return fields[r.tag]
	}
	for _, c := range r.children {
		if present(c, fields) {
			return true
		}
	}
	return false
}
func (d *Dictionary) required(rules []*rule, fields map[int]bool) *ValidationError {
	for _, r := range rules {
		if r.component {
			if r.required || present(r, fields) {
				if v := d.required(r.children, fields); v != nil {
					return v
				}
			}
		} else if r.required && !fields[r.tag] {
			return d.failure(r.tag, "1", "Required tag missing")
		}
	}
	return nil
}

// Validate checks dictionary membership, datatypes, enums, duplicates and each
// repeating-group scope. User fields (>=5000) are allowed unless strict is set.
func (d *Dictionary) Validate(m *Message, strict bool) *ValidationError {
	body, known := d.messages[m.MsgType()]
	if !known {
		return nil
	} // application handler supplies 35=j for unsupported MsgTypes
	rules := append(append(append([]*rule{}, d.header...), body...), d.trailer...)
	allowed := map[int]*rule{}
	var collect func([]*rule)
	collect = func(rs []*rule) {
		for _, r := range flatten(rs) {
			allowed[r.tag] = r
			collect(r.children)
		}
	}
	collect(rules)
	tags := m.Tags
	if len(tags) == 0 {
		keys := make([]int, 0, len(m.Fields))
		for k := range m.Fields {
			keys = append(keys, k)
		}
		sort.Ints(keys)
		for _, k := range keys {
			tags = append(tags, Field{k, m.Fields[k]})
		}
	}
	header, trailer := map[int]bool{}, map[int]bool{}
	var mark func([]*rule, map[int]bool)
	mark = func(rs []*rule, m map[int]bool) {
		for _, r := range flatten(rs) {
			m[r.tag] = true
			mark(r.children, m)
		}
	}
	mark(d.header, header)
	mark(d.trailer, trailer)
	zone := 0
	for _, f := range tags {
		if header[f.Tag] {
			if zone > 0 {
				return d.failure(f.Tag, "14", "Tag specified out of required order")
			}
		} else if trailer[f.Tag] {
			zone = 2
		} else {
			if zone == 2 {
				return d.failure(f.Tag, "14", "Body field after trailer")
			}
			zone = 1
		}
	}

	for _, f := range tags {
		if f.Value == "" {
			return d.failure(f.Tag, "4", "Tag specified without a value")
		}

		def, ok := d.fields[f.Tag]
		if !ok {
			if f.Tag >= 5000 && !strict {
				continue
			}
			return d.failure(f.Tag, "0", "Invalid tag number")
		}
		if _, ok := allowed[f.Tag]; !ok {
			return d.failure(f.Tag, "2", "Tag not defined for this message type")
		}
		if f.Value == "" {
			return d.failure(f.Tag, "4", "Tag specified without a value")
		}
		if !validType(def.Type, f.Value) {
			return d.failure(f.Tag, "6", "Incorrect data format for value")
		}
		switch def.Type {
		case "BOOLEAN":
			if f.Value != "Y" && f.Value != "N" {
				return d.failure(f.Tag, "5", "Value is incorrect (out of range)")
			}
		case "LENGTH", "SEQNUM", "NUMINGROUP", "DAYOFMONTH":
			n, _ := strconv.ParseInt(f.Value, 10, 64)
			if n < 0 {
				return d.failure(f.Tag, "5", "Value is incorrect (out of range)")
			}
		}
		if len(def.Values) > 0 {
			values := []string{f.Value}
			if strings.HasPrefix(def.Type, "MULTIPLE") {
				values = strings.Fields(f.Value)
			}
			for _, value := range values {
				found := false
				for _, v := range def.Values {
					if value == v.Enum {
						found = true
						break
					}
				}
				if !found {
					return d.failure(f.Tag, "5", "Value is incorrect (out of range)")
				}
			}
		}
	}
	_, v := d.scope(tags, 0, rules, false, strict)
	return v
}
func (d *Dictionary) scope(tags []Field, pos int, rules []*rule, group, strict bool) (int, *ValidationError) {
	flat := flatten(rules)
	ranks := map[int]int{}
	byTag := map[int]*rule{}
	for i, r := range flat {
		ranks[r.tag] = i
		byTag[r.tag] = r
	}
	seen := map[int]bool{}
	last := -1
	delimiter := 0
	if len(flat) > 0 {
		delimiter = flat[0].tag
	}
	for pos < len(tags) {
		f := tags[pos]
		r, ok := byTag[f.Tag]
		if !ok {
			if _, defined := d.fields[f.Tag]; !defined && f.Tag >= 5000 && !strict {
				if seen[f.Tag] {
					return pos, d.failure(f.Tag, "13", "Tag appears more than once")
				}
				seen[f.Tag] = true
				pos++
				continue
			}
			break
		}
		if group && f.Tag == delimiter && seen[delimiter] {
			break
		}
		if seen[f.Tag] {
			return pos, d.failure(f.Tag, "13", "Tag appears more than once")
		}
		rank := ranks[f.Tag]
		if group && rank < last {
			return pos, d.failure(f.Tag, "15", "Repeating group fields out of order")
		}
		if group && len(seen) == 0 && f.Tag != delimiter {
			return pos, d.failure(f.Tag, "15", "Repeating group delimiter missing or out of order")
		}
		last = rank
		seen[f.Tag] = true
		pos++
		if len(r.children) > 0 {
			n, _ := strconv.Atoi(f.Value)
			if n < 0 {
				return pos, d.failure(f.Tag, "5", "Negative NumInGroup")
			}
			child := flatten(r.children)
			delim := child[0].tag
			for i := 0; i < n; i++ {
				if pos >= len(tags) || tags[pos].Tag != delim {
					for _, cr := range child {
						if pos < len(tags) && tags[pos].Tag == cr.tag {
							return pos, d.failure(tags[pos].Tag, "15", "Repeating group fields out of order")
						}
					}
					return pos, d.failure(f.Tag, "16", "Incorrect NumInGroup count")
				}
				var v *ValidationError
				pos, v = d.scope(tags, pos, r.children, true, strict)
				if v != nil {
					return pos, v
				}
			}
			if pos < len(tags) && tags[pos].Tag == delim {
				return pos, d.failure(f.Tag, "16", "Incorrect NumInGroup count")
			}
		}
	}
	if v := d.required(rules, seen); v != nil {
		return pos, v
	}
	if !group && pos < len(tags) {
		return pos, d.failure(tags[pos].Tag, "15", "Field outside repeating group")
	}
	return pos, nil
}
func validType(typ, v string) bool {
	switch typ {
	case "INT", "LENGTH", "SEQNUM", "NUMINGROUP", "DAYOFMONTH":
		if !isDigits(strings.TrimPrefix(v, "-")) {
			return false
		}
		_, e := strconv.ParseInt(v, 10, 64)
		return e == nil
	case "PRICE", "AMT", "QTY", "FLOAT", "PRICEOFFSET", "PERCENTAGE":
		decimal := strings.TrimPrefix(v, "-")
		dots, digits := 0, 0
		for _, c := range decimal {
			if c == '.' {
				dots++
			} else if c >= '0' && c <= '9' {
				digits++
			} else {
				return false
			}
		}
		if dots > 1 || digits == 0 {
			return false
		}
		n, e := strconv.ParseFloat(v, 64)
		return e == nil && !math.IsNaN(n) && !math.IsInf(n, 0)
	case "CHAR":
		return len(v) == 1
	case "BOOLEAN":
		return len(v) == 1
	case "UTCTIMESTAMP":
		_, e := time.Parse("20060102-15:04:05.999999999", v)
		return e == nil
	case "UTCTIMEONLY":
		_, e := time.Parse("15:04:05.999999999", v)
		return e == nil
	case "UTCDATEONLY", "LOCALMKTDATE":
		_, e := time.Parse("20060102", v)
		return e == nil
	case "MONTHYEAR":
		if len(v) != 6 && len(v) != 8 {
			return false
		}
		if _, e := time.Parse("200601", v[:6]); e != nil {
			return false
		}
		if len(v) == 6 {
			return true
		}
		if v[6] == 'w' {
			return v[7] >= '1' && v[7] <= '5'
		}
		_, e := time.Parse("20060102", v)
		return e == nil
	}
	return true
}

// RequiredFields remains a dictionary-derived view for existing test builders.
type RequiredField struct {
	Tag  int
	Name string
}

var RequiredFields = func() map[string][]RequiredField {
	out := map[string][]RequiredField{}
	for _, typ := range []string{"D", "F", "G", "q", "V"} {
		var walk func([]*rule, bool)
		walk = func(rs []*rule, required bool) {
			for _, r := range rs {
				if r.component {
					walk(r.children, required && r.required)
				} else if required && r.required {
					out[typ] = append(out[typ], RequiredField{r.tag, DefaultDictionary.fields[r.tag].Name})
					if len(r.children) > 0 {
						walk(r.children, true)
					}
				}
			}
		}
		walk(DefaultDictionary.messages[typ], true)
	}
	return out
}()

func MissingRequired(m *Message) (RequiredField, bool) {
	for _, f := range RequiredFields[m.MsgType()] {
		if m.Get(f.Tag) == "" {
			return f, true
		}
	}
	return RequiredField{}, false
}

// ValidateIdentity distinguishes missing/empty identifiers from a nonempty
// authenticated identity mismatch before routing to CompID enforcement.
func (d *Dictionary) ValidateIdentity(m *Message) *ValidationError {
	for _, tag := range []int{49, 56} {
		value, ok := m.Fields[tag]
		if !ok {
			return d.failure(tag, "1", "Required tag missing")
		}
		if value == "" {
			return d.failure(tag, "4", "Tag specified without a value")
		}
	}
	return nil
}
