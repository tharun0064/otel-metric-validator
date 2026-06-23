FROM python:3.12-slim

WORKDIR /app

# Install deps first for layer caching
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

# App code
COPY validator/ ./validator/

# Default: run as a service. Override the command for one-shot, e.g.
#   docker run ... --rm validator            (one-shot)
#   docker compose run --rm validator --json  (one-shot json)
ENTRYPOINT ["python", "-m", "validator.cli"]
CMD ["--watch"]
