// Command sqlq runs SQL against skyq.db and prints the results as
// tab-separated text, for pasting diagnostics out of the NUC. With no query
// arguments it runs the baseline diagnostics below. It opens the database
// read-only unless -write is given; with -write each argument is executed
// and its affected row count printed.
//
//	go run ./cmd/sqlq -db C:\skyq\skyq.db
//	go run ./cmd/sqlq -db C:\skyq\skyq.db "SELECT * FROM nights"
//	go run ./cmd/sqlq -db C:\skyq\skyq.db -write "DELETE FROM baselines WHERE ..."
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	_ "modernc.org/sqlite"
)

var diagnostics = []string{
	`SELECT * FROM baselines ORDER BY median_stars LIMIT 25`,
	`SELECT night_of, cloud_onset_at, baseline_mode, ingested_at FROM nights ORDER BY night_of DESC LIMIT 10`,
	`SELECT n.night_of, f.target, f.filter, COUNT(*) AS lights,
	        MIN(f.detected_stars) AS min_stars, MAX(f.detected_stars) AS max_stars
	   FROM frames f JOIN nights n ON n.id = f.night_id
	  WHERE f.class = 'light'
	  GROUP BY n.night_of, f.target, f.filter
	  ORDER BY n.night_of DESC, f.target, f.filter
	  LIMIT 60`,
	`SELECT n.night_of, f.at, f.target, f.filter, f.detected_stars, f.hfr
	   FROM frames f JOIN nights n ON n.id = f.night_id
	  WHERE f.class = 'light' AND f.detected_stars < 20
	  ORDER BY f.at DESC
	  LIMIT 40`,
}

func main() {
	path := flag.String("db", "skyq.db", "path to skyq.db")
	write := flag.Bool("write", false, "execute the statements with write access")
	flag.Parse()
	if _, err := os.Stat(*path); err != nil {
		log.Fatalf("database: %v", err)
	}

	// Without -write, mode=ro plus query_only: never write by accident.
	dsn := fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(5000)&_pragma=query_only(1)", *path)
	if *write {
		dsn = fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)", *path)
	}
	d, err := sql.Open("sqlite", dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer d.Close()

	queries := flag.Args()
	if *write {
		if len(queries) == 0 {
			log.Fatal("-write needs at least one statement")
		}
		for _, q := range queries {
			if err := exec(d, q); err != nil {
				log.Fatalf("ERROR: %v", err) // stop: later statements may depend on this one
			}
		}
		return
	}
	if len(queries) == 0 {
		queries = diagnostics
	}
	for _, q := range queries {
		if err := run(d, q); err != nil {
			fmt.Printf("ERROR: %v\n\n", err)
		}
	}
}

func exec(d *sql.DB, q string) error {
	fmt.Printf("== %s\n", strings.Join(strings.Fields(q), " "))
	r, err := d.Exec(q)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	fmt.Printf("(%d rows affected)\n\n", n)
	return nil
}

func run(d *sql.DB, q string) error {
	fmt.Printf("== %s\n", strings.Join(strings.Fields(q), " "))
	rows, err := d.Query(q)
	if err != nil {
		return err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	fmt.Println(strings.Join(cols, "\t"))
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	n := 0
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		out := make([]string, len(cols))
		for i, v := range vals {
			switch v := v.(type) {
			case nil:
				out[i] = "NULL"
			case []byte:
				out[i] = string(v)
			default:
				out[i] = fmt.Sprint(v)
			}
		}
		fmt.Println(strings.Join(out, "\t"))
		n++
	}
	fmt.Printf("(%d rows)\n\n", n)
	return rows.Err()
}
