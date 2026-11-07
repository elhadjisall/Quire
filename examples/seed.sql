-- run with: kvdb exec data/app.db "$(cat examples/seed.sql)"
CREATE TABLE users (
  id INT64,
  name STRING,
  email STRING,
  age INT64,
  PRIMARY KEY (id),
  INDEX (email),
  INDEX (age, name)
);

CREATE TABLE posts (
  user_id INT64,
  post_id INT64,
  title STRING,
  body STRING,
  PRIMARY KEY (user_id, post_id),
  INDEX (title)
);

INSERT INTO users VALUES
  (1, 'ada',   'ada@lovelace.io',   36),
  (2, 'linus', 'linus@kernel.org',  54),
  (3, 'grace', 'grace@navy.mil',    36),
  (4, 'ken',   'ken@bell-labs.com', 80),
  (5, 'alan',  'alan@bletchley.uk', 41);

INSERT INTO posts VALUES
  (1, 1, 'Notes on the analytical engine', 'Sketch of the engine...'),
  (1, 2, 'On loops',                       'A note about repetition.'),
  (2, 1, 'Just for fun',                   'Hello everybody out there.'),
  (3, 1, 'A compiler',                     'The A-0 system.');

SELECT name, age FROM users WHERE age BETWEEN 30 AND 60 ORDER BY age;
SELECT post_id, title FROM posts WHERE user_id = 1;
