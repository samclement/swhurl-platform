package rules

// The offered documents: what an instance's OpenAPI and AsyncAPI documents, kept beside it in Git, say it answers and
// publishes. The appsource adapter decodes them into these types; the console's pages word them.

// Offers is what one instance's offered documents say: its API, its events, or why they could not be read. An
// instance with no documents offers nothing, which is not a problem.
type Offers struct {
	API     *OfferedAPI
	Events  []OfferedEvent
	Problem string
}

// OfferedAPI is what an OpenAPI document says an app answers.
type OfferedAPI struct {
	Description string
	Operations  []Operation
}

// Operation is one route of an API: its method, its path, and what the document says it does.
type Operation struct{ Method, Path, Summary string }

// OfferedEvent is one event an app publishes, as its AsyncAPI document describes it.
type OfferedEvent struct {
	Name, Description string
	Payload, Example  Value // its payload schema, for the field rows, and the first example a payload document gives
}

// ValueKind is what a Value holds.
type ValueKind int

// The kinds a document's value has; the zero value is an absent one, which a page shows as nothing.
const (
	ValueAbsent ValueKind = iota
	ValueNull
	ValueString
	ValueNumber
	ValueBool
	ValueArray
	ValueObject
)

// Value is a document's value with an object's keys in the order the document writes them, so a payload schema's
// fields are shown as the app wrote them. A scalar keeps the text JSON prints it as, except a string, which keeps its
// own text and is quoted when the page prints it.
type Value struct {
	Kind   ValueKind
	Fields []ValueField // an object's keys, in document order
	Items  []Value      // an array's entries
	Text   string       // a scalar: a string's text, a number, true, false or null
}

// ValueField is one key of an object.
type ValueField struct {
	Name  string
	Value Value
}

// APIText is what an app's OpenAPI document says about its API, empty when it describes none.
func (o Offers) APIText() string {
	if o.API == nil {
		return ""
	}
	return o.API.Description
}

// EventText is what an app's AsyncAPI document says about one of its events, empty when it says nothing.
func (o Offers) EventText(name string) string {
	for _, event := range o.Events {
		if event.Name == name {
			return event.Description
		}
	}
	return ""
}
