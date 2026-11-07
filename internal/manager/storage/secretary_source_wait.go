package storage

import (
	"context"
	"database/sql"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func clearSecretarySourceWaitLocked(job *memorySecretaryJob) {
	if job.lastError == domain.ErrSecretarySourceIncomplete.Error() {
		job.lastError = ""
	}
}

func commitPreparedSecretaryBatchTx(ctx context.Context, tx *sql.Tx, source string) error {
	_, err := tx.ExecContext(ctx, `UPDATE secretary_jobs SET last_error=''
 WHERE source_id=$1 AND last_error=$2`, source, domain.ErrSecretarySourceIncomplete.Error())
	if err != nil {
		return err
	}
	return tx.Commit()
}
