from pydantic_settings import BaseSettings, SettingsConfigDict
from typing import Optional

class AppSettings(BaseSettings):
    # 配置环境变量读取，支持 .env 文件（可选）
    model_config = SettingsConfigDict(env_file=".env", env_file_encoding="utf-8")

    # 服务配置，带默认值
    SERVICE: str = "app"
    ENV: str = "dev"
    PORT: int = 8000
    VERSION: Optional[str] = "1.0.0"  # 优先从环境变量读取，默认 1.0.0

# 单例配置实例
settings = AppSettings()