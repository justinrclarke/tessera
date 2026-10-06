FROM python:3.13-alpine
RUN apk add --no-cache postgresql-client
WORKDIR /app
COPY app.py .
EXPOSE 8080
CMD ["python", "-u", "app.py"]
