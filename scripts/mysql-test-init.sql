-- Dedicated application account. Root is reserved for test provisioning.
CREATE USER IF NOT EXISTS 'target_user'@'%' IDENTIFIED BY 'test-password';
GRANT SELECT, INSERT, UPDATE, DELETE, CREATE, DROP, REFERENCES, INDEX, ALTER,
    CREATE VIEW, SHOW VIEW, TRIGGER ON `target\_test`.* TO 'target_user'@'%';
