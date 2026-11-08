-- extra statements for a database created with examples/seed.sql
SHOW INDEX FROM users;
EXPLAIN SELECT name, age FROM users WHERE age BETWEEN 30 AND 60 ORDER BY age;

SELECT COUNT(*), MIN(age), MAX(age), SUM(age)
FROM users
WHERE name LIKE 'a%';

SELECT name FROM users ORDER BY name LIMIT 3 OFFSET 1;

CREATE INDEX ON users (name);
SELECT id, email FROM users WHERE name = 'ada';
DROP INDEX ON users (name);

BEGIN;
INSERT INTO users VALUES (6, 'edsger', 'edsger@utexas.edu', 70);
ROLLBACK;
