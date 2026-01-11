-- Add column to store IPQS JSON response when IP abuse is the rejection reason
ALTER TABLE signup_rejections ADD COLUMN ipqs_response_json TEXT;
