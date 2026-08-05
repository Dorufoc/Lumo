// db-tool 是 sqlite3 CLI 的进程内替代工具（本机未安装 sqlite3，且项目使用纯 Go 驱动）。
// 用于数据库 schema 检查与 QA 场景（Verification strategy / QA 3/4/5/13）。
//
// 用法：
//   go run ./cmd/db-tool [-db PATH] [-migrate=false] schema
//   go run ./cmd/db-tool [-db PATH] [-migrate=false] exec "SQL"
//
// DB 路径解析：-db 优先；否则复用 internal/config 数据目录逻辑
// （LUMO_DATA_DIR 或默认 %APPDATA%\lumo），打开 <DataDir>/lumo.db。
// 默认 -migrate=true：打开后先执行待应用迁移（与应用启动行为一致）；-migrate=false 只读检查。
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"strings"

	"lumo/internal/config"
	"lumo/internal/database"

	_ "modernc.org/sqlite"
)

func main() {
	dbPath := flag.String("db", "", "SQLite 数据库路径（默认 <DataDir>/lumo.db）")
	migrate := flag.Bool("migrate", true, "打开后自动执行待应用迁移（false=只读检查）")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "db-tool: SQLite 数据库检查工具（schema / exec）")
		fmt.Fprintln(os.Stderr, "用法: db-tool [-db PATH] [-migrate=false] schema|exec \"SQL\"")
		flag.PrintDefaults()
	}
	flag.Parse()

	path := *dbPath
	if path == "" {
		path = config.Load().DBPath
	}

	db, err := database.Open(path)
	if err != nil {
		fatal("打开数据库 %s: %v", path, err)
	}
	defer db.Close()

	if *migrate {
		if err := database.Migrate(context.Background(), db); err != nil {
			fatal("迁移 %s: %v", path, err)
		}
	}

	args := flag.Args()
	if len(args) == 0 {
		flag.Usage()
		os.Exit(2)
	}

	switch args[0] {
	case "schema":
		if err := dumpSchema(db); err != nil {
			fatal("schema: %v", err)
		}
	case "exec":
		if len(args) < 2 {
			flag.Usage()
			os.Exit(2)
		}
		if err := execSQL(db, args[1]); err != nil {
			fatal("exec %q: %v", args[1], err)
		}
	default:
		fmt.Fprintf(os.Stderr, "未知子命令 %q（可用: schema, exec）\n", args[0])
		os.Exit(2)
	}
}

// dumpSchema 输出 .schema 等价物：全部表 / 索引 / 触发器的 CREATE 语句。
func dumpSchema(db *sql.DB) error {
	rows, err := db.Query(`
		SELECT type, name, sql FROM sqlite_master
		WHERE name NOT LIKE 'sqlite_%'
		ORDER BY type, name`)
	if err != nil {
		return err
	}
	defer rows.Close()
	lastType := ""
	for rows.Next() {
		var typ, name string
		var sqlTxt sql.NullString
		if err := rows.Scan(&typ, &name, &sqlTxt); err != nil {
			return err
		}
		if typ != lastType {
			label := strings.ToUpper(typ[:1]) + typ[1:]
			fmt.Printf("\n-- %s --\n", label)
			lastType = typ
		}
		if sqlTxt.Valid {
			fmt.Println(sqlTxt.String + ";")
		} else {
			fmt.Printf("-- %s\n", name)
		}
	}
	return rows.Err()
}

// execSQL 执行任意 SQL；返回结果集时以管道分隔打印，否则打印受影响行数。
func execSQL(db *sql.DB, q string) error {
	stmt := strings.TrimSpace(q)
	if stmt == "" {
		return fmt.Errorf("空 SQL")
	}

	rows, err := db.Query(stmt)
	if err == nil {
		defer rows.Close()
		cols, cerr := rows.Columns()
		if cerr != nil {
			return cerr
		}
		fmt.Println(strings.Join(cols, "|"))
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		count := 0
		for rows.Next() {
			if err := rows.Scan(ptrs...); err != nil {
				return err
			}
			strs := make([]string, len(cols))
			for i, v := range vals {
				if v == nil {
					strs[i] = "NULL"
				} else {
					strs[i] = fmt.Sprintf("%v", v)
				}
			}
			fmt.Println(strings.Join(strs, "|"))
			count++
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if count == 0 {
			fmt.Println("(0 rows)")
		}
		return nil
	}

	res, exErr := db.Exec(stmt)
	if exErr != nil {
		return fmt.Errorf("query: %v; exec: %w", err, exErr)
	}
	affected, _ := res.RowsAffected()
	fmt.Printf("OK, %d row(s) affected\n", affected)
	return nil
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
