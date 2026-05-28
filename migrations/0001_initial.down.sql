-- 0001_initial.down.sql — reverse of up. Dropped in FK-safe order.
DROP TABLE IF EXISTS session_state;
DROP TABLE IF EXISTS deliveries;
DROP TABLE IF EXISTS user_settings;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS post_embeddings;
DROP TABLE IF EXISTS posts;
DROP TABLE IF EXISTS clusters;
DROP TABLE IF EXISTS channels;
