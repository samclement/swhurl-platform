package orchestration

import (
	"github.com/samclement/swhurl-platform/v2/adapters/appsource"
	"github.com/samclement/swhurl-platform/v2/producer"
)

// ImageDeclaration reads the app's named sources and composes one truthful image label, without creation validation.
func ImageDeclaration(reader appsource.Reader, root string) (string, error) {
	doc, err := reader.Declaration(root)
	if err != nil {
		return "", err
	}
	s, err := producer.Parse(doc)
	if err != nil {
		return "", err
	}
	documents := []producer.Document{}
	if s.APIFile != "" {
		data, err := reader.API(root, s.APIFile)
		if err != nil {
			return "", err
		}
		documents = append(documents, producer.Document{Role: "api", Bytes: data})
	}
	publications := []string{}
	if s.EventsFile != "" {
		events, data, err := reader.Events(root, s.EventsFile)
		if err != nil {
			return "", err
		}
		publications, err = producer.Publications(events, s.EventsFile)
		if err != nil {
			return "", err
		}
		documents = append(documents, producer.Document{Role: "events", Bytes: data})
	}
	if len(s.Flags) > 0 {
		documents = append(documents, producer.Document{Role: "flags", Bytes: producer.FlagsDocument(s.Flags)})
	}
	return producer.Label(s, publications, documents)
}
