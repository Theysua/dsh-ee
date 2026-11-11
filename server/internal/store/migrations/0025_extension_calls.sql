-- 声明式扩展（docs/plugins.md §5.1、§7.6）：连接是扩展装出来的时候，每次调用记下扩展的「身份@版本」
-- 和清单（连同接口文档）的 sha256。扩展升级之后，回头查一次调用还能知道当时跑的是哪一版。
ALTER TABLE connection_calls
  ADD COLUMN extension VARCHAR(160) NOT NULL DEFAULT '',
  ADD COLUMN digest    VARCHAR(64)  NOT NULL DEFAULT '';
