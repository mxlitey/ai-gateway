-- 费用字段去掉「美元」含义：数值就是用户自己在价格表里填的单位，
-- 具体是哪种货币由面板的「货币符号」决定，库里只存纯数字。
ALTER TABLE request_logs RENAME COLUMN cost_prompt_usd TO cost_prompt;
ALTER TABLE request_logs RENAME COLUMN cost_completion_usd TO cost_completion;
ALTER TABLE request_logs RENAME COLUMN cost_usd TO cost;
