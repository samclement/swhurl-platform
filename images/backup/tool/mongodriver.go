package main

import (
	"context"
	"fmt"
	"sort"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// driverFacts reads what a restore test compares: the server's version, and each collection's document count.
// It connects with the job's own connection string and reads only; the platform's backups grant nothing else.
type driverFacts struct{}

// Facts reads one database's version and counts. The timeout bounds server selection, as the Python job's
// driver does, so a server that is not there fails the job rather than hanging it.
func (driverFacts) Facts(ctx context.Context, uri, database string) (string, map[string]int64, error) {
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	client, err := mongo.Connect(bounded, options.Client().ApplyURI(uri))
	if err != nil {
		return "", nil, fmt.Errorf("cannot connect")
	}
	defer func() { _ = client.Disconnect(context.Background()) }()
	var version struct {
		Version string `bson:"version"`
	}
	if err := client.Database("admin").RunCommand(bounded, bson.D{{Key: "buildInfo", Value: 1}}).Decode(&version); err != nil {
		return "", nil, fmt.Errorf("cannot read the server version")
	}
	names, err := client.Database(database).ListCollectionNames(bounded, bson.D{})
	if err != nil {
		return "", nil, fmt.Errorf("cannot list the collections")
	}
	sort.Strings(names)
	counts := make(map[string]int64, len(names))
	for _, name := range names {
		count, err := client.Database(database).Collection(name).CountDocuments(bounded, bson.D{})
		if err != nil {
			return "", nil, fmt.Errorf("cannot count a collection")
		}
		counts[name] = count
	}
	return version.Version, counts, nil
}
