package project

import "github.com/C1ph3rX13/Aether/internal/database"

// DBTX is the concrete SQLite transaction boundary used by Project commands
// and queries. Business SQL remains in this package.
type DBTX = database.DBTX
