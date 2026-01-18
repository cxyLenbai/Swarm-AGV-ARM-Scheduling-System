package repos // 仓储包

import ( // 依赖导入
	"context" // 上下文处理

	"github.com/jackc/pgx/v5"        // pgx 接口
	"github.com/jackc/pgx/v5/pgconn" // 连接命令结果
)

type DBTX interface { // 数据库事务/连接抽象
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error) // 执行语句
	Query(context.Context, string, ...any) (pgx.Rows, error)         // 查询多行
	QueryRow(context.Context, string, ...any) pgx.Row                // 查询单行
}
