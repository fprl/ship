from django.http import HttpResponse, JsonResponse
from django.urls import path


def health(_request):
    return HttpResponse("ok", content_type="text/plain")


def index(_request):
    return JsonResponse({"app": "django-sqlite", "database_path": "/data/db.sqlite3"})


urlpatterns = [
    path("health", health),
    path("", index),
]

