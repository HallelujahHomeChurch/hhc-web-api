ALTER TABLE hhc_web.member_recording ADD COLUMN description text NOT NULL DEFAULT '' CHECK (char_length(description) <= 5000);
