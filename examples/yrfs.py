#!/usr/bin/python3

import requests
import hashlib
import urllib3

# 忽略SSL警告
urllib3.disable_warnings(urllib3.exceptions.InsecureRequestWarning)
def get_url(url, headers=None, params=None):
    if headers is None:
        headers = {}
    if params is None:
        params = {}
    try:
        # 发送GET请求获取license
        response = requests.get(url, headers=headers, params=params, verify=False)
        # 检查响应码
        response.raise_for_status()
        out_json = response.json()
        if out_json:
            return out_json
        else:
            return False
    except requests.exceptions.RequestException as e:
        print(e)

class YanRongCloudFile(object):
    def __init__(self):
        self.username = "admin"
        self.password = "Passw0rd"
        self.md5hash = ""
        self.token = ""
        self.url = "https://192.168.73.25"
    # token密文生成
    def calculate_md5_twice(self, string):
        md5_hash1 = hashlib.md5()
        md5_hash1.update(string.encode('utf-8'))
        md5_value1 = md5_hash1.hexdigest()
        md5_hash2 = hashlib.md5()
        md5_hash2.update(md5_value1.encode('utf-8'))
        md5_value2 = md5_hash2.hexdigest()
        merged_value = ''
        for c1, c2 in zip(md5_value1, md5_value2):
            merged_value += c1 + c2
        self.md5hash = merged_value
    # 获取token
    def get_token(self):
        self.calculate_md5_twice(self.password)
        date = {
            "username": self.username,
            "password": self.md5hash
        }
        try:
            # 发送POST请求获取token
            response = requests.post(self.url + "/api/auth/tokens", json=date, verify=False)
            # 检查响应码
            response.raise_for_status()
            # 解析响应的JSON数据
            token_data = response.json()
            # 提取token
            self.token = token_data["data"]["token"]
            if self.token:
                 return True
            else:
                return False
        except requests.exceptions.RequestException as e:
            print(f"请求token时发生错误: {e}")
    def get_quota(self, path):
        url = self.url + "/api/v3/quotas"
        detail_headers = {
            "x-auth-token": self.token,
        }
        data = {
            "page": "1",
            "page_size": 10,
            "lang": "zh",
            "key": path
        }
        output = get_url(url, headers=detail_headers, params=data)
        return output

def main():
    x= YanRongCloudFile()
    x.get_token()
    print( x.get_quota("/quota_test"))
if __name__ == '__main__':
    main()
