package format

import (
	"errors"
	"io/fs"
	"os"

	"github.com/selectiveci/selective-cli/internal/contract"
)

func WriteSecretFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return contract.Errorf(contract.UsageError, "%s already exists; remove it or choose another --output path", path)
	}
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
