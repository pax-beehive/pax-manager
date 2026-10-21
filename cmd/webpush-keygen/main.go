package main

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/pax-beehive/pax-manager/internal/manager/notifications"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("webpush-keygen", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("out", "", "New private JSON file (mode 0600); must not already exist")
	subscriber := flags.String("subscriber", "", "VAPID contact: mailto:address or HTTPS URL")
	keyID := flags.String("key-id", "webpush-"+rand.Text(), "Stable key identifier")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *path == "" || *keyID == "" || len(*keyID) > 128 {
		return errors.New("provide -out, -subscriber and a valid -key-id")
	}
	private, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	config := notifications.WebPushConfig{
		PublicKey:  base64.RawURLEncoding.EncodeToString(private.PublicKey().Bytes()),
		PrivateKey: base64.RawURLEncoding.EncodeToString(private.Bytes()),
		Subscriber: *subscriber,
	}
	if err := config.Validate(); err != nil {
		return err
	}
	file, err := os.OpenFile(*path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	encoded := json.NewEncoder(file)
	encoded.SetIndent("", "  ")
	writeErr := encoded.Encode(
		map[string]string{
			"public_key":  config.PublicKey,
			"private_key": config.PrivateKey,
			"subscriber":  config.Subscriber,
			"key_id":      *keyID,
		},
	)
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		_ = os.Remove(*path)
		return err
	}
	_, err = fmt.Fprintln(out, "Created private Web Push key file:", *path)
	return err
}
