### This fork aim to support histfile without timestamp.

# go-histdbimport

An tool for importing old zsh history into [histdb](https://github.com/larkery/zsh-histdb)

## Environment Variable
For histfile without timestamp, current timestamp will be used as timestamp for every entry, this will make the order of the commands in database after import different from the histfile, to preserve this order, use env `PRESERVE_ORDER`.
```shell
$ export PRESERVE_ORDER=true
```

By default, this tool will read the default path of both histfile and db (`$HOME/.zsh_history` and `$HOME/.histdb/zsh-history.db`), to change this use `DB_PATH` and `HISTORY_PATH`.
```shell
$ export DB_PATH=/home/user/zsh_history.db
$ export HISTORY_PATH=/home/user/.histfile
```
If for some reason importing directly into currently using db (`$HOME/.histdb/zsh-history.db`) success but `histdb` return error, try import into `template.db` and replace instead.<br>
**Remember to take backup of the current histfile and db**

## Compile from source
Edit `main.go` if needed
```shell
$ git clone https://github.com/FuLygon/go-histdbimport.git && cd go-histdbimport
$ go build main.go
```

### License

    /* This Source Code Form is subject to the terms of the Mozilla Public
     * License, v. 2.0. If a copy of the MPL was not distributed with this
     * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

