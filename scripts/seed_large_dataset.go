// Jalankan dengan PGHOST, PGPORT, PGDATABASE, PGUSER, dan PGPASSWORD milik target uji.
package main

import (
	"context"
	_ "embed"
	"fmt"
	"log"
	"os"
	"os/signal"

	"github.com/jackc/pgx/v5"
)

//go:embed seed_large_dataset.sql
var seed string

func main() {
	if os.Getenv("PGDATABASE") == "" {
		log.Fatal("set PGDATABASE ke database target pengujian")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	conn, err := pgx.Connect(ctx, "")
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close(context.Background())
	if _, err := conn.Exec(ctx, seed); err != nil {
		log.Fatal(err)
	}
	fmt.Println("500000 akun uji tersedia di public.benchmark_accounts")
}
