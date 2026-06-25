// Command backuper-server (gencerts.go) — генерация самоподписанных CA и
// сертификатов сервера/клиента для mTLS (раздел 6.1, 24.2 ТЗ).
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"backuper/internal/certs"
)

func cmdGenCerts(args []string) {
	fs := flag.NewFlagSet("gen-certs", flag.ExitOnError)
	outDir := fs.String("out", "certs", "директория вывода")
	clientHost := fs.String("client-host", "", "IP/hostname клиента-слушателя для SAN (через запятую) — обязательно")
	serverHost := fs.String("server-host", "", "IP/hostname сервера для SAN (через запятую)")
	days := fs.Int("days", 3650, "срок действия сертификатов в днях")
	fs.Parse(args)

	if *clientHost == "" {
		fmt.Fprintln(os.Stderr, "ОШИБКА: укажите -client-host (адрес клиента в LAN)")
		os.Exit(2)
	}
	if err := certs.WriteAll(*outDir, splitHosts(*serverHost), splitHosts(*clientHost), *days); err != nil {
		fmt.Fprintf(os.Stderr, "ОШИБКА генерации сертификатов: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Сертификаты созданы в %s: ca.crt, ca.key, server.crt, server.key, client.crt, client.key\n", *outDir)
	fmt.Println("CA (ca.crt) скопируйте обеим сторонам; приватные ключи — только своей стороне.")
}

func splitHosts(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
