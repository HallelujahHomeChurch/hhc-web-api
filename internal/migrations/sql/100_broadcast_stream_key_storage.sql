ALTER TABLE hhc_web.member_recording_broadcast_source
 ADD COLUMN key_ciphertext bytea,
 ADD COLUMN key_encryption_id text NOT NULL DEFAULT '';
ALTER TABLE hhc_web.member_recording_broadcast_source
 ADD CONSTRAINT stream_key_envelope_pair CHECK ((key_ciphertext IS NULL AND key_encryption_id='') OR (octet_length(key_ciphertext)>=28 AND key_encryption_id<>''));
