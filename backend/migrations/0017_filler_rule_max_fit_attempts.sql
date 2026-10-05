-- Tail and fallback fills try other candidates when a picked item does not fit.
--
-- filler_rule.max_fit_attempts is how many candidates in a row such a fill may
-- reject for not fitting the time left before it gives up. 1 (the default)
-- gives up at the first miss, which is what every existing rule did; the API
-- bounds it at 20. It is only read for tail and fallback rules.
ALTER TABLE filler_rule ADD COLUMN max_fit_attempts INTEGER NOT NULL DEFAULT 1 CHECK (max_fit_attempts BETWEEN 1 AND 20);
