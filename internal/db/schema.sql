CREATE TABLE IF NOT EXISTS users (
    id TEXT PRIMARY KEY,
    email TEXT NOT NULL COLLATE NOCASE UNIQUE,
    username TEXT NOT NULL COLLATE NOCASE UNIQUE,
    password_hash TEXT NOT NULL,
    created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions (
    user_id TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    expires_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS sessions_expiry ON sessions(expires_at);
CREATE TABLE IF NOT EXISTS categories (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS posts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id TEXT NOT NULL REFERENCES users(id),
    title TEXT NOT NULL CHECK(length(trim(title)) BETWEEN 1 AND 160),
    content TEXT NOT NULL CHECK(length(trim(content)) BETWEEN 1 AND 10000),
    created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS posts_recent ON posts(created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS posts_author ON posts(user_id, created_at DESC);
CREATE TABLE IF NOT EXISTS post_categories (
    post_id INTEGER NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
    category_id INTEGER NOT NULL REFERENCES categories(id),
    PRIMARY KEY(post_id, category_id)
);
CREATE INDEX IF NOT EXISTS post_categories_category ON post_categories(category_id, post_id);
CREATE TABLE IF NOT EXISTS comments (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    post_id INTEGER NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
    user_id TEXT NOT NULL REFERENCES users(id),
    content TEXT NOT NULL CHECK(length(trim(content)) BETWEEN 1 AND 5000),
    created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS comments_post ON comments(post_id, id);
CREATE INDEX IF NOT EXISTS comments_author ON comments(user_id);
CREATE TABLE IF NOT EXISTS post_reactions (
    post_id INTEGER NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    value INTEGER NOT NULL CHECK(value IN (-1, 1)),
    PRIMARY KEY(post_id, user_id)
);
CREATE INDEX IF NOT EXISTS post_reactions_user ON post_reactions(user_id, value, post_id);
CREATE TABLE IF NOT EXISTS comment_reactions (
    comment_id INTEGER NOT NULL REFERENCES comments(id) ON DELETE CASCADE,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    value INTEGER NOT NULL CHECK(value IN (-1, 1)),
    PRIMARY KEY(comment_id, user_id)
);
INSERT OR IGNORE INTO categories(id, name, description) VALUES
    (1, 'Engineering', 'Go, SQL, systems and the craft of reliable software.'),
    (2, 'Tomorrow School', 'Peer learning, projects and discoveries along the way.'),
    (3, 'Martial Arts', 'Daily practice, focus and a beginner’s mind.'),
    (4, 'Service & Leadership', 'Responsibility, teamwork and lessons from service.'),
    (5, 'Astana Tech', 'Ideas, technology and Kazakhstan’s innovation community.');
PRAGMA user_version = 1;
