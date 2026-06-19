package main

import (
	"log"
	"os"

	"gorm.io/driver/postgres"
	"gorm.io/gen"
	"gorm.io/gorm"
)

const defaultDatabaseURL = "postgres://pax:pax@localhost:5432/paxdb?sslmode=disable"

func main() {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		databaseURL = defaultDatabaseURL
	}

	db, err := gorm.Open(postgres.Open(databaseURL), &gorm.Config{})
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	initSQL, err := os.ReadFile("db/init.sql")
	if err != nil {
		log.Fatalf("read init.sql: %v", err)
	}
	if err := db.Exec(string(initSQL)).Error; err != nil {
		log.Fatalf("ensure schema: %v", err)
	}

	g := gen.NewGenerator(gen.Config{
		OutPath:           "internal/manager/storage/dal/query",
		ModelPkgPath:      "internal/manager/storage/dal/model",
		Mode:              gen.WithDefaultQuery | gen.WithQueryInterface,
		FieldNullable:     true,
		FieldCoverable:    true,
		FieldSignable:     true,
		FieldWithIndexTag: true,
		FieldWithTypeTag:  true,
	})
	g.UseDB(db)
	g.ApplyBasic(g.GenerateAllTable()...)
	g.Execute()
}
