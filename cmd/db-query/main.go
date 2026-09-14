// Command db-query runs ad-hoc read-only diagnostics against db/game.db.
package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"

	_ "modernc.org/sqlite"
)

func main() {
	dsn := "file:db/game.db?mode=ro"
	if len(os.Args) > 2 {
		dsn = os.Args[2]
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	if len(os.Args) < 2 {
		log.Fatal("usage: db-query <sql>")
	}
	rows, err := db.Query(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}
	defer rows.Close()

	cols, _ := rows.Columns()
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			log.Fatal(err)
		}
		for i, c := range cols {
			v := vals[i]
			if b, ok := v.([]byte); ok {
				v = string(b)
			}
			fmt.Printf("%s=%v ", c, v)
		}
		fmt.Println()
	}
}
