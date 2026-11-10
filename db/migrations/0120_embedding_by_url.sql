-- Embeddings come from an external OpenAI-compatible service chosen by the
-- operator. The vector columns lose their fixed dimension, and each vector
-- records the model that wrote it. Search compares only vectors from the
-- configured model; the background job re-embeds the rest.

create extension if not exists vector;

alter table work_memory add column if not exists embedding_model text;
alter table skill_embedding add column if not exists embedding_model text;
alter table library_concept add column if not exists embedding_model text;

update work_memory set embedding_model = 'Xenova/all-MiniLM-L6-v2'
  where embedding is not null and embedding_model is null;
update skill_embedding set embedding_model = 'Xenova/all-MiniLM-L6-v2'
  where embedding is not null and embedding_model is null;
update library_concept set embedding_model = 'Xenova/all-MiniLM-L6-v2'
  where embedding is not null and embedding_model is null;

alter table work_memory alter column embedding type vector;
alter table skill_embedding alter column embedding type vector;
alter table library_concept alter column embedding type vector;

create index if not exists work_memory_text_fts_idx
  on work_memory using gin (to_tsvector('english', text));
create index if not exists skill_embedding_document_fts_idx
  on skill_embedding using gin (to_tsvector('english', document));
