-- Reverse of 003_oci.up.sql. Drops in FK-safe order.
DROP TABLE IF EXISTS docker_tags;
DROP TABLE IF EXISTS docker_manifests;
DROP TABLE IF EXISTS docker_blobs;
DROP TABLE IF EXISTS blob_upload_sessions;
