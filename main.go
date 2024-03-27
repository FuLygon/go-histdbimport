/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */
package main

import (
	"bufio"
	"bytes"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

// used for dir column
var unknownDir string

// used for host column
var hostName string

// used for session column
var sessionNum = "0"

// used for exit_status column
var retVal = "0"

// representation of a history entry
type basicEntry struct {
	started  string //no reason to convert to uint64
	duration string
	cmd      string
}

var boringCommands = strings.Join([]string{
	"cd",
	"ls",
	"top",
	"htop",
}, ",")

// location of database file
var databaseFile string

// location of history file
var historyFile string

func init() {
	host, err := os.Hostname()
	if err != nil {
		host = "UNKNOWN"
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
	}
	flag.StringVar(&databaseFile, "database", filepath.Join(home, ".histdb/zsh-history.db"),
		"location of database file")
	flag.StringVar(&historyFile, "history", filepath.Join(home, ".zsh_history"),
		"location of history file")
	flag.StringVar(&boringCommands, "ignore", boringCommands, "commands to ignore during import")
	flag.StringVar(&hostName, "host", host, "value for host column")
	flag.StringVar(&unknownDir, "dir", home, "directory used for command import")
}

// Reads the entry, traversing multiple lines if needed
func readEntry(s *bufio.Scanner, buf *bytes.Buffer) (string, bool, error) {
	var ok bool
	entry := ""
	for {
		ok = s.Scan()
		if !ok {
			break
		}

		if buf != nil {
			// write line back to buf to recreate scanner later
			_, err := fmt.Fprintln(buf, s.Text())
			if err != nil {
				return "", false, err
			}
		}

		entry += s.Text()
		entryLen := len(entry)
		if entryLen == 0 {
			break
		}
		//multiline cmds end with slash
		if entry[entryLen-1] == '\\' {
			//trim the slash and restore the new line
			entry = entry[:entryLen-1] + "\n"
			continue
		}
		break
	}
	return entry, ok, nil
}

// Parses an entry string into a basicEntry
func parseEntry(entry string, timestamp int64) (basicEntry, error) {
	var entryInfo basicEntry

	data := strings.SplitN(entry, ";", 2)
	if data == nil || len(data) != 2 {
		return basicEntry{}, errors.New("Unable to parse entry= " + entry)
	}

	if len(data) == 2 {
		// processing histfile with timestamp
		info := strings.Split(data[0], ":")
		if info == nil || len(info) != 3 {
			return basicEntry{}, errors.New("Unable to parse timestamp=" + data[0])
		}

		entryInfo.started = strings.TrimSpace(info[1])
		entryInfo.duration = strings.TrimSpace(info[2])
		entryInfo.cmd = data[1]
	} else {
		// processing histfile without timestamp
		entryInfo.started = fmt.Sprintf("%d", timestamp)
		entryInfo.duration = "0"
		entryInfo.cmd = entry
	}

	return entryInfo, nil
}

type transaction struct {
	*sql.Tx
	cmdStmt   *sql.Stmt
	placeStmt *sql.Stmt
	histStmt  *sql.Stmt
}

func beginTransaction(db *sql.DB) (txx *transaction, err error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	t := &transaction{Tx: tx}
	defer func() {
		if err != nil {
			if t.cmdStmt != nil {
				t.cmdStmt.Close()
			}
			if t.placeStmt != nil {
				t.placeStmt.Close()
			}
			if t.histStmt != nil {
				t.histStmt.Close()
			}
			t.Rollback()
		}
	}()

	/*
	   insert into commands (argv) values (${cmd});
	   insert into places   (host, dir) values (${HISTDB_HOST}, ${pwd});
	   insert into history
	     (session, command_id, place_id, exit_status, start_time, duration)
	   select
	     ${HISTDB_SESSION},
	     commands.rowid,
	     places.rowid,
	     ${retval},
	     ${started},
	     ${now} - ${started}
	   from
	     commands, places
	   where
	     commands.argv = ${cmd} and
	     places.host = ${HISTDB_HOST} and
	     places.dir = ${pwd}
	   ;
	*/
	t.cmdStmt, err = t.Prepare("INSERT INTO commands (argv) VALUES (?);")
	if err != nil {
		return nil, err
	}
	t.placeStmt, err = t.Prepare("INSERT INTO places (host, dir) VALUES (?, ?);")
	if err != nil {
		return nil, err
	}
	t.histStmt, err = t.Prepare(`
		INSERT INTO history (session, command_id, place_id, exit_status, start_time, duration)
			SELECT ?, commands.rowid, places.rowid, ?, ?, ?
			FROM commands, places
			WHERE commands.argv = ? AND places.host = ? AND places.dir = ?;
	`)
	if err != nil {
		return nil, err
	}

	return t, nil
}

func (t *transaction) insertEntry(entry basicEntry) (err error) {
	_, err = t.cmdStmt.Exec(entry.cmd)
	if err != nil {
		return err
	}
	_, err = t.placeStmt.Exec(hostName, unknownDir)
	if err != nil {
		return err
	}
	_, err = t.histStmt.Exec(sessionNum, retVal, entry.started, entry.duration, entry.cmd, hostName, unknownDir)
	if err != nil {
		return err
	}

	return nil
}

func main() {
	flag.Parse()

	db, err := sql.Open("sqlite3", databaseFile)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	tx, err := beginTransaction(db)
	if err != nil {
		log.Fatal(err)
	}

	fd, err := os.Open(historyFile)
	if err != nil {
		log.Fatal(err)
	}
	defer fd.Close()

	err = readAndInsert(tx, fd, true)
	if err != nil {
		tx.Rollback()
		log.Fatal(err)
	}

	err = tx.Commit()
	if err != nil {
		log.Fatal(err)
	}
}

func readAndInsert(tx *transaction, r io.Reader, preserveOrder bool) (err error) {
	var forwardTimestamp int64

	// use currentTimestamp as timestamp for commands if histfile doesn't contain timestamp
	currentTimestamp := time.Now().Unix()

	r = transform.NewReader(r, unicode.UTF8.NewDecoder())
	scanner := bufio.NewScanner(r)

	bcs := strings.Split(boringCommands, ",")

	// if preserving order, rewind currentTimestamp based on total inserted entry into db
	if preserveOrder {
		currentTimestamp, err = rewindTimestamp(scanner, bcs, currentTimestamp)
	}

outer:
	for {
		if err = scanner.Err(); err != nil {
			return err
		}

		entry, ok, err := readEntry(scanner, nil)
		switch {
		case err != nil:
			return err
		case !ok:
			break outer
		case entry == "":
			continue outer
		}

		// use forwardTimestamp to add second to currentTimestamp after rewinding
		if preserveOrder {
			currentTimestamp = currentTimestamp + forwardTimestamp
		}

		parsed, err := parseEntry(entry, currentTimestamp)
		if err != nil {
			return err
		}

		for _, bc := range bcs {
			if parsed.cmd == bc {
				log.Printf("Skipping %+v\n", parsed)
				continue outer
			}
		}

		log.Printf("Inserting %+v\n", parsed)
		err = tx.insertEntry(parsed)
		if err != nil {
			return err
		}

		if preserveOrder {
			forwardTimestamp++
		}
	}

	return nil
}

func rewindTimestamp(scanner *bufio.Scanner, bcs []string, currentTimestamp int64) (int64, error) {
	var (
		lineCount int64
		buf       bytes.Buffer
	)

	// replicate loop of readAndInsert() to count total entry need to be inserted
outer:
	for {
		if err := scanner.Err(); err != nil {
			return 0, err
		}

		entry, ok, err := readEntry(scanner, &buf)
		switch {
		case err != nil:
			return 0, err
		case !ok:
			break outer
		case entry == "":
			continue outer
		}

		parsed, err := parseEntry(entry, currentTimestamp)
		if err != nil {
			return 0, err
		}

		for _, bc := range bcs {
			if parsed.cmd == bc {
				continue outer
			}
		}

		lineCount++
	}

	// recreate scanner after read
	*scanner = *bufio.NewScanner(&buf)
	return currentTimestamp - lineCount, nil
}
