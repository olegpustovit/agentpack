-- Local record of Main epochs this machine handed over: a released Main stops refreshing and
-- pushing before the new main_node row reaches it.
CREATE TABLE main_release (
    epoch INTEGER NOT NULL PRIMARY KEY
);
